// Author: paulangton
// Copyright 2020

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

// LibraryService is responsible for interfacing with the potentials-utils local
// spotify library
type LibraryService struct {
	CacheDir     string
	CacheName    string
	libraryIndex *SpotifyLibraryIndex
}

func NewLibraryService(cacheDir string) (*LibraryService, error) {
	libraryService := &LibraryService{
		CacheDir:     cacheDir,
		CacheName:    "library.json",
		libraryIndex: &SpotifyLibraryIndex{},
	}

	err := libraryService.readyLibrary()
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
	mode := os.FileMode(int(755))
	bytes, err := json.Marshal(s.libraryIndex)
	if err != nil {
		return err
	}
	err = os.MkdirAll(s.CacheDir, mode)
	err = ioutil.WriteFile(path.Join(s.CacheDir, s.CacheName), bytes, mode)
	if err != nil {
		return err
	}
	return nil
}

func (s *LibraryService) readyLibrary() error {
	if s.libraryIndex.Alive() {
		return nil
	}
	cache, err := NewSpotifyLibraryIndexFromFile(s.CacheDir)
	if err == SpotifyLibraryIndexCreateError {
		cache, err = NewSpotifyLibraryIndex()
		if err != nil {
			return err
		}
	}
	if err != nil {
		return err
	}
	s.libraryIndex = cache
	return nil

}

// GetByID returns the corresponding SavedTRack for the provided key if it exists and the cache is
// fresh. Will rebuild the cache if stale.
func (s *LibraryService) GetByID(k spotify.ID) (*spotify.SavedTrack, error) {
	err := s.readyLibrary()
	if err != nil {
		return nil, err
	}
	v := s.libraryIndex.tracksByID[k]
	return v, nil
}

// GetBySongArtistAlbum gets all tracks with the same song name, artist name, and album title
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

// NewSpotifyLibraryIndexFromFile creates a SpotifyLibraryIndex from a saved
// cache on disk
func NewSpotifyLibraryIndexFromFile(path string) (*SpotifyLibraryIndex, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, SpotifyLibraryIndexCreateError
	}
	slurp, err := ioutil.ReadAll(file)
	if err != nil {
		return nil, SpotifyLibraryIndexCreateError
	}
	var index *SpotifyLibraryIndex
	err = json.Unmarshal(slurp, &index)
	if err != nil {
		return nil, err
	}
	return index, nil

}

// NewSpotifyLibraryIndex creates a SpotifyLibraryIndex with a cache lifetime of 1 day.
func NewSpotifyLibraryIndex() (*SpotifyLibraryIndex, error) {
	err := AuthMe()
	if err != nil {
		return nil, err
	}
	i := &SpotifyLibraryIndex{
		tracksByID:      map[spotify.ID]*spotify.SavedTrack{},
		trackSearchTree: prefixtree.NewPrefixTree(),
		lifetime:        config.Cache.Lifetime,
		evictionTime:    time.Now(), // Eviction time will be incremented by lifetime once cache is ready
	}
	err = i.Rebuild()
	if err != nil {
		return nil, err
	}
	return i, nil

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

func (i *SpotifyLibraryIndex) indexTrack(k spotify.ID, v spotify.SavedTrack) {
	i.tracksByID[k] = &v
	i.addTrackToSearchTree(v)
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

func (i *SpotifyLibraryIndex) Rebuild() error {

	// Cache has expired, need to rebuild
	log.Printf("Rebuilding Spotify library cache index...")
	trackPager, err := spClient.CurrentUsersTracks()
	if err != nil {
		return err
	}
	for {
		log.Printf("Built %d/%d tracks...", trackPager.Offset, trackPager.Total)
		for _, t := range trackPager.Tracks {
			i.indexTrack(t.ID, t)
		}
		err := spClient.NextPage(trackPager)
		if err != nil {
			if err != spotify.ErrNoMorePages {
				return err
			}
			break
		}
	}
	i.evictionTime = time.Now().Add(i.lifetime)
	log.Printf("Successfully built library cache of %d tracks.", len(i.tracksByID))
	return nil

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
	log.Printf("Got token %v", token)
	// create a client using the specified token
	c := auth.NewClient(token)
	clientCh <- &c
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("200 - OK"))
	log.Printf("Created client, auth flow complete")
}

// HandleCleanPotentials cleans my potentials playlist. It removes all songs i have already saved in
// my library from the playlist.
func HandleCleanPotentials(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("200 - OK"))
	log.Printf("%s - OK", r.URL.Path)
	cleaned, err := cleanPotentials(false)
	if err != nil {
		log.Printf("Error cleaning potentials playlist: %v", err)
		return
	}
	log.Printf("Successfully cleaned %d tracks from the potentials playlist.", cleaned)
}

func cleanPotentials(dryRun bool) (int, error) {
	// Fetch the potentials playlist
	playlist, err := spClient.GetPlaylist(config.Spotify.PotentialsPlaylistID)
	if err != nil {
		return 0, err
	}

	// Clean the playlist page by page cross-referencing the library cache
	pager := &playlist.Tracks
	numCleaned := 0
	for {
		_, numCleanedForPage, err := cleanPotentialsPage(pager.Tracks, config.Spotify.PotentialsPlaylistID, dryRun)
		if err != nil {
			return 0, err
		}
		numCleaned += numCleanedForPage
		err = spClient.NextPage(pager)
		if err != nil {
			if err != spotify.ErrNoMorePages {
				return 0, err
			}
			break
		}
	}
	return numCleaned, nil

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

// cleanPotentialsPage also returns the number of duplicate tracks cleaned on the given page
func cleanPotentialsPage(page []spotify.PlaylistTrack, playlistID spotify.ID, dryRun bool) (spotify.ID, int, error) {
	duplicateTracks := []spotify.PlaylistTrack{}
	for _, playlistTrack := range page {
		trackID := playlistTrack.Track.ID
		// first try to get the track by ID
		libraryTrack, err := libraryService.GetByID(trackID)
		if err != nil {
			return spotify.ID(""), 0, err
		}
		if libraryTrack != nil {
			// track is already in our library, remove it
			// log.Printf("Found a duplicate track in the potentials playlist: %s by %s off the album %s (ID: %s).", track.Track.Name, track.Track.Artists[0].Name, track.Track.Album.Name, trackID)
			duplicateTracks = append(duplicateTracks, playlistTrack)
			continue
		}
		// if aggressive cleaning, try to match the track metadata to something in our library
		if config.Duplicates.Aggressive {
			duplicateLibraryTracks, err := libraryService.GetBySongAlbumArtistNames(playlistTrack.Track.Name, playlistTrack.Track.Album.Name, getArtistNames(playlistTrack.Track.SimpleTrack))
			if err != nil {
				return spotify.ID(""), 0, err
			}
			// Means we found at least one library track which is a
			// name-album-artist duplicate
			if len(duplicateLibraryTracks) > 0 {
				duplicateTracks = append(duplicateTracks, playlistTrack)
			}
		}
	}

	if dryRun {
		for _, t := range duplicateTracks {
			log.Printf("[DUPLICATE] %s", TrackString(t.Track))
		}
		return playlistID, len(duplicateTracks), nil
	} else {
		ids := []spotify.ID{}
		for _, t := range duplicateTracks {
			ids = append(ids, t.Track.ID)
		}
		snapshot, err := spClient.RemoveTracksFromPlaylist(playlistID, ids...)
		if err != nil {
			return spotify.ID(snapshot), 0, err
		}
		// Return playlistID of new (mutated) playlist for next request.
		return spotify.ID(snapshot), len(duplicateTracks), nil
	}
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
	libraryService, err = NewLibraryService(config.Cache.CacheDir)
	if err != nil {
		log.Fatalf("Failed to start the potentials-utils library service, error: %v", err)
	}
	auth = spotify.NewAuthenticator(config.Spotify.CallbackURL, spotify.ScopeUserReadPrivate, spotify.ScopePlaylistReadPrivate, spotify.ScopePlaylistModifyPublic, spotify.ScopePlaylistModifyPrivate, spotify.ScopeUserLibraryRead)
	// Stupid library reads by default from environment variables so we have to
	// manually set credentials here.
	auth.SetAuthInfo(config.Spotify.ID, config.Spotify.Secret)
	rand.Seed(time.Now().UTC().UnixNano())

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
			log.Printf("Found %d duplicate tracks in the potentials playlist.", cleaned)
		} else {
			log.Printf("Successfully cleaned %d tracks from the potentials playlist.", cleaned)
		}
	}

}
