package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"math/rand"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v2"

	"potentials-utils/prefixtree"

	"github.com/zmb3/spotify"
)

var (
	clientCh       = make(chan *spotify.Client)
	auth           spotify.Authenticator
	spClient       *spotify.Client
	libraryService *LibraryService
	sessionKey     string
	config         *PotentialsUtilsConfig
	cfgPath        string
	runserver      bool
	dryRun         bool
	noCache        bool
)

var SpotifyLibraryIndexCreateError = errors.New("Error creating Spotify library cache")

type CacheConfig struct {
	Lifetime time.Duration `yaml:"lifetimeNs"`
	CacheDir string        `yaml:"cacheDir"`
}

// DuplicatesConfig holds config options for potentials-utils' duplicate
// detection behavior
type DuplicatesConfig struct {
	// Aggressive controls cleaning aggression levels. If true, enables more
	// aggressive cleaning which will remove tracks from Potentials which match
	// the song name, album name, and all artist names of an existing track in
	// your library. Tracks will onlly be removed by ID otherwise.
	Aggressive bool `yaml:"aggressive"`
}

type SpotifyConfig struct {
	ID                   string        `yaml:"id"`
	Secret               string        `yaml:"secret"`
	CallbackURL          string        `yaml:"callbackURL"`
	User                 string        `yaml:"user"`
	PotentialsPlaylistID spotify.ID    `yaml:"potentialsPlaylistID"`
	AuthTimeout          time.Duration `yaml:"authTimeoutNs"`
}

type PotentialsUtilsConfig struct {
	Spotify    SpotifyConfig    `yaml:"spotify"`
	Duplicates DuplicatesConfig `yaml:"duplicates"`
	Cache      CacheConfig      `yaml:"cache"`
}

// StoredLibrary is a serialization type for storing a library on disk
type StoredLibrary struct {
	Expiration time.Time            `json:"expiration,omitempty"`
	Tracks     []spotify.SavedTrack `json:"tracks,omitempty"`
}

// LibraryService is responsible for interfacing with the potentials-utils local
// spotify library
type LibraryService struct {
	CacheDir     string
	CacheFile    string
	libraryIndex *SpotifyLibraryIndex
}

// NewStoredLibrary creates a new StoredLibrary with sensible defaults
func NewStoredLibrary() *StoredLibrary {
	return &StoredLibrary{
		Expiration: time.Now(),
		Tracks:     []spotify.SavedTrack{},
	}
}

// NewLibraryService creates a new LibraryService instance. The instance will
// attempt to build its cache from the provided cache directory.
func NewLibraryService(cacheDir string) (*LibraryService, error) {
	err := AuthMe()
	if err != nil {
		return nil, err
	}
	libraryService := &LibraryService{
		CacheDir:     cacheDir,
		CacheFile:    path.Join(cacheDir, "library.json"),
		libraryIndex: &SpotifyLibraryIndex{},
	}

	err = libraryService.readyLibrary()
	if err != nil {
		return nil, err
	}
	err = libraryService.persistLibrary()
	if err != nil {
		return nil, err
	}
	return libraryService, nil
}

func (s *LibraryService) persistLibrary() error {
	mode := os.FileMode(uint32(0755))
	storedLibrary := NewStoredLibrary()
	storedLibrary.Expiration = s.libraryIndex.evictionTime
	for _, v := range s.libraryIndex.tracksByID {
		storedLibrary.Tracks = append(storedLibrary.Tracks, *v)
	}
	bytes, err := json.Marshal(storedLibrary)
	if err != nil {
		return err
	}
	err = os.MkdirAll(s.CacheDir, mode)
	if err != nil {
		return err
	}
	err = ioutil.WriteFile(s.CacheFile, bytes, mode)
	if err != nil {
		return err
	}
	return nil
}

func (s *LibraryService) readyLibrary() error {
	if s.libraryIndex.Alive() {
		return nil
	}
	if err := s.indexFromCacheFile(); err != nil {
		log.Printf("Failed to build index from cache, error: %v", err)
		if err = s.indexFromSpotify(); err != nil {
			return err
		}
	}
	return nil
}

func (s *LibraryService) indexFromSpotify() error {
	index := NewSpotifyLibraryIndex()
	log.Printf("Rebuilding Spotify library cache index...")
	trackPager, err := spClient.CurrentUsersTracks()
	if err != nil {
		return err
	}
	for {
		log.Printf("Built %d/%d tracks...", trackPager.Offset, trackPager.Total)
		for _, t := range trackPager.Tracks {
			index.IndexTrack(t.ID, t)
		}
		err := spClient.NextPage(trackPager)
		if err != nil {
			if err != spotify.ErrNoMorePages {
				return err
			}
			break
		}
	}
	index.MakeItFresh()
	s.libraryIndex = index
	return nil
}

func (s *LibraryService) indexFromCacheFile() error {
	index := NewSpotifyLibraryIndex()
	file, err := os.Open(s.CacheFile)
	if err != nil {
		return err
	}
	slurp, err := ioutil.ReadAll(file)
	if err != nil {
		return err
	}
	var storedLibrary *StoredLibrary
	err = json.Unmarshal(slurp, &storedLibrary)
	if err != nil {
		return err
	}
	for _, t := range storedLibrary.Tracks {
		index.IndexTrack(t.ID, t)
	}
	index.evictionTime = storedLibrary.Expiration
	s.libraryIndex = index
	return nil
}

// GetByID returns the corresponding SavedTrack for the provided key if it exists and the cache is
// fresh. Will rebuild the cache if stale.
func (s *LibraryService) GetByID(k spotify.ID) (*spotify.SavedTrack, error) {
	err := s.readyLibrary()
	if err != nil {
		return nil, err
	}
	v := s.libraryIndex.tracksByID[k]
	return v, nil
}

// GetBySongArtistAlbum gets all tracks with the same song name, artist name,
// and album title. Will rebuild cache if stale.
func (s *LibraryService) GetBySongAlbumArtistNames(songName, albumName string, artistNames []string) ([]*spotify.SavedTrack, error) {
	err := s.readyLibrary()
	if err != nil {
		return nil, err
	}
	searchStr := trackIndexString(songName, albumName, artistNames)
	if s.libraryIndex.trackSearchTree.Contains(searchStr) {
		// search entire cache for songs that match these fields
		var matches []*spotify.SavedTrack
		for _, v := range s.libraryIndex.tracksByID {
			if v.Name == songName && v.Album.Name == albumName && containsAll(getArtistNames(v.SimpleTrack), artistNames) {
				matches = append(matches, v)
			}
		}
		return matches, nil
	} else {
		return nil, nil
	}

}

// SpotifyLibraryIndex represents an in-memory cache of the current users' spotify library. It must
// be completely rebuilt if the current time is after the evictionTime. Yeah I
// know this is basically a hand-tuned database, I did it for fun go read a book
type SpotifyLibraryIndex struct {
	tracksByID      map[spotify.ID]*spotify.SavedTrack `json:"items"`
	trackSearchTree *prefixtree.PrefixTree             `json:"searchTree"`
	lifetime        time.Duration                      `json:"lifetime"`
	// This cache has to be completely rebuilt, no element-wise evictions
	evictionTime time.Time `json:"evictionTime"`
}

func (c *SpotifyLibraryIndex) dumpTree() []string {
	return c.trackSearchTree.Words()
}

// NewSpotifyLibraryIndex creates a SpotifyLibraryIndex with a cache lifetime of 1 day.
func NewSpotifyLibraryIndex() *SpotifyLibraryIndex {
	return &SpotifyLibraryIndex{
		tracksByID:      map[spotify.ID]*spotify.SavedTrack{},
		trackSearchTree: prefixtree.NewPrefixTree(),
		lifetime:        config.Cache.Lifetime,
		evictionTime:    time.Now(), // Eviction time will be
	}

}

func trackIndexString(trackName, albumName string, artistNames []string) string {
	var indexStrBuilder strings.Builder
	// Song name
	indexStrBuilder.WriteString(fmt.Sprintf("%s", trackName))
	// Album name
	indexStrBuilder.WriteString(fmt.Sprintf("%s", albumName))
	// Each artist name, in alphabetical order
	sort.Strings(artistNames)
	for _, a := range artistNames {
		indexStrBuilder.WriteString(fmt.Sprintf("%s", a))
	}
	return indexStrBuilder.String()
}

// addTrackToSearchTree adds tracks to the search tree using a custom track
// string "[TrackName][AlbumName][ArtistNames...]"
func (i *SpotifyLibraryIndex) addTrackToSearchTree(v spotify.SavedTrack) {
	searchTerm := trackIndexString(v.Name, v.Album.Name, getArtistNames(v.SimpleTrack))
	i.trackSearchTree.Add(searchTerm)
}

// IndexTrack adds a track to the library index and refreshes the lifetime of
// the index
func (i *SpotifyLibraryIndex) IndexTrack(k spotify.ID, v spotify.SavedTrack) {
	i.tracksByID[k] = &v
	i.addTrackToSearchTree(v)
}

// MakeItFresh tells the library index it should be considered fresh
func (i *SpotifyLibraryIndex) MakeItFresh() {
	i.evictionTime = time.Now().Add(i.lifetime)
}

func containsAll(list1, list2 []string) bool {
	if len(list2) != len(list2) {
		return false
	}

	containsAll := true
	for _, e1 := range list1 {
		found := false
		for _, e2 := range list2 {
			found = found || e1 == e2
		}
		containsAll = containsAll && found
	}
	return containsAll

}

func (i *SpotifyLibraryIndex) Alive() bool {
	return time.Now().Before(i.evictionTime)
}

func authServer() *http.Server {
	mux := http.NewServeMux()

	mux.HandleFunc("/callback/spotify", HandleAuthCallback)
	mux.HandleFunc("/spotify/cleanpotentials", HandleCleanPotentials)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		log.Println("Got request for:", r.URL.String())
	})
	srv := &http.Server{
		Addr:    ":8080",
		Handler: mux,
	}
	return srv

}

// AuthMe authenticates with Spotify as me and creates a client or uses the current client
// if already authenticated.
func AuthMe() error {
	// no auth server up, start a one-off
	if !runserver {
		log.Printf("running one-off auth server")
		authSrv := authServer()
		go func() {
			err := authSrv.ListenAndServe()
			if err == http.ErrServerClosed {
			} else {

			}
		}()
		ctx, cancelFunc := context.WithTimeout(context.Background(), config.Spotify.AuthTimeout)
		defer cancelFunc()
		defer authSrv.Shutdown(ctx)
		err := authMeWithTimeout()
		if err != nil {
			return err
		}

	}
	if spClient == nil {
		err := authMeWithTimeout()
		if err != nil {
			return err
		}
	}
	if _, err := spClient.CurrentUser(); err != nil {
		err = authMeWithTimeout()
		if err != nil {
			return err
		}
		// The current client works, just use it.
		log.Printf("The current Spotify client is authenticated.")
	}
	return nil
}

func authMeWithTimeout() error {
	// problems if there is ever more than one auth request in flight
	sessionKey = fmt.Sprintf("potentials-session-key-%d", rand.Intn(10000))
	url := auth.AuthURL(sessionKey)
	fmt.Printf("Visit %s in a browser to complete the authentication process.\n", url)
	timeoutCh := make(chan bool)
	go func() {
		time.Sleep(config.Spotify.AuthTimeout)
		timeoutCh <- true
	}()
	select {
	case c := <-clientCh:
		spClient = c
		log.Printf("Authenticated successfully with Spotify.")
		return nil
	case <-timeoutCh:
		return fmt.Errorf("Authentication timed out.")

	}
}

// HandleAuthCallback handles the Spotify OAuth2.0 callback and passes on an auth'd client
func HandleAuthCallback(w http.ResponseWriter, r *http.Request) {
	// use the same state string here that you used to generate the URL
	token, err := auth.Token(sessionKey, r)
	if err != nil {
		log.Printf("Received auth callback, failed.")
		http.Error(w, fmt.Sprintf("Couldn't get token from sessionkey %s, request %v", sessionKey, r), http.StatusNotFound)
		return
	}
	// create a client using the specified token
	c := auth.NewClient(token)
	clientCh <- &c
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("200 - OK"))
	log.Printf("Created client, auth flow complete")
}

// HandleCleanPotentials cleans my Potentials playlist. It removes all songs i have already saved in
// my library from the playlist.
func HandleCleanPotentials(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("200 - OK"))
	log.Printf("%s - OK", r.URL.Path)
	cleaned, err := cleanPotentials(false)
	if err != nil {
		log.Printf("Error cleaning Potentials playlist: %v", err)
		return
	}
	log.Printf("Successfully cleaned %d tracks from the Potentials playlist.", cleaned)
}

// cleanPotentials removes duplicate tracks from the configured spotify
// potentials playlist
func cleanPotentials(dryRun bool) (int, error) {
	// Fetch the Potentials playlist
	playlist, err := spClient.GetPlaylist(config.Spotify.PotentialsPlaylistID)
	if err != nil {
		return 0, err
	}
	log.Printf("Cleaning Potentials playlist ID: %s", playlist.ID)

	// Clean the playlist page by page cross-referencing the library cache
	pager := &playlist.Tracks
	duplicates := []spotify.PlaylistTrack{}
	for {
		duplicatesInPage, err := getDuplicates(pager.Tracks)
		if err != nil {
			return 0, err
		}
		duplicates = append(duplicates, duplicatesInPage...)
		if err = spClient.NextPage(pager); err != nil {
			if err == spotify.ErrNoMorePages {
				break
			}
			return 0, err
		}
	}
	ids := []spotify.ID{}
	for _, t := range duplicates {
		log.Printf("[DUPLICATE] %s", TrackString(t.Track))
		ids = append(ids, t.Track.ID)
	}
	if !dryRun {
		// Assuming this is atomic... the first returned value is the new playlist
		// snapshot for future requests, unused for now
		_, err := spClient.RemoveTracksFromPlaylist(config.Spotify.PotentialsPlaylistID, ids...)
		if err != nil {
			return 0, err
		}
	}
	return len(duplicates), nil
}

// TrackString prints a human-readable summary of a spotify track
func TrackString(t spotify.FullTrack) string {
	artistString := ""
	for ix, a := range t.Artists {
		if ix == len(t.Artists)-1 {
			artistString += fmt.Sprintf("%s", a.Name)
		} else {
			artistString += fmt.Sprintf("%s, ", a.Name)
		}
	}
	return fmt.Sprintf("%s, %s, on %s released %s, Track ID: %s\n", t.Name, artistString, t.Album.Name, t.Album.ReleaseDate, t.ID)
}

func getArtistNames(t spotify.SimpleTrack) []string {
	var names []string
	for _, a := range t.Artists {
		names = append(names, a.Name)
	}
	return names
}

// getDuplicates finds all tracks in the provided list of playlist tracks which
// are duplicated in your library. Duplication detection is by ID by default,
// but can be done by title-artist-album by specifying `--aggressive`.
func getDuplicates(page []spotify.PlaylistTrack) ([]spotify.PlaylistTrack, error) {
	duplicateTracks := []spotify.PlaylistTrack{}
	for _, playlistTrack := range page {
		trackID := playlistTrack.Track.ID
		// first try to get the track by ID
		libraryTrack, err := libraryService.GetByID(trackID)
		if err != nil {
			return []spotify.PlaylistTrack{}, err
		}
		if libraryTrack != nil {
			// track is already in our library, remove it
			// log.Printf("Found a duplicate track in the Potentials playlist: %s by %s off the album %s (ID: %s).", track.Track.Name, track.Track.Artists[0].Name, track.Track.Album.Name, trackID)
			duplicateTracks = append(duplicateTracks, playlistTrack)
			continue
		}
		// if aggressive cleaning, try to match the track metadata to something in our library
		if config.Duplicates.Aggressive {
			duplicateLibraryTracks, err := libraryService.GetBySongAlbumArtistNames(playlistTrack.Track.Name, playlistTrack.Track.Album.Name, getArtistNames(playlistTrack.Track.SimpleTrack))
			if err != nil {
				return []spotify.PlaylistTrack{}, err
			}
			// Means we found at least one library track which is a
			// name-album-artist duplicate
			if len(duplicateLibraryTracks) > 0 {
				duplicateTracks = append(duplicateTracks, playlistTrack)
			}
		}
	}

	return duplicateTracks, nil
}

func main() {

	flag.BoolVar(&runserver, "runserver", false, "runs potentials-utils in server mode")
	flag.BoolVar(&dryRun, "dry-run", false, "prints tracks that would be deleted from Potentials instead of removing them if true")
	flag.BoolVar(&noCache, "no-cache", false, "If true, invalidates your library cache and rebuilds it from scratch")
	flag.StringVar(&cfgPath, "config", "config.yaml", "Path to potentials-utils config file")
	flag.Parse()

	contents, err := ioutil.ReadFile(cfgPath)
	if err != nil {
		log.Fatalf("Config file at %s not found.", cfgPath)
	}
	err = yaml.Unmarshal(contents, &config)
	if err != nil {
		log.Fatalf("Failed to unmarshal YAML config, err: %v", err)
	}
	auth = spotify.NewAuthenticator(config.Spotify.CallbackURL, spotify.ScopeUserReadPrivate, spotify.ScopePlaylistReadPrivate, spotify.ScopePlaylistModifyPublic, spotify.ScopePlaylistModifyPrivate, spotify.ScopeUserLibraryRead)
	// Stupid library reads by default from environment variables so we have to
	// manually set credentials here.
	auth.SetAuthInfo(config.Spotify.ID, config.Spotify.Secret)
	rand.Seed(time.Now().UTC().UnixNano())

	libraryService, err = NewLibraryService(config.Cache.CacheDir)
	if err != nil {
		log.Fatalf("Failed to start the potentials-utils library service, error: %v", err)
	}
	if runserver {
		log.Printf("Server UP")
		authSrv := authServer()
		log.Fatal(authSrv.ListenAndServe())
	} else {
		if dryRun {
			log.Printf("Running cleanPotentials in dry-run mode. No tracks will be deleted from your playlist.")
		}
		cleaned, err := cleanPotentials(dryRun)
		if err != nil {
			log.Fatalf(err.Error())
		}
		if dryRun {
			log.Printf("Found %d duplicate tracks in the Potentials playlist.", cleaned)
		} else {
			log.Printf("Successfully cleaned %d tracks from the Potentials playlist.", cleaned)
		}
	}

}
