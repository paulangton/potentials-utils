package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"math/rand"
	"net/http"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v2"

	"potentials-utils/prefixtree"

	"github.com/apex/log"
	"github.com/cheggaaa/pb/v3"
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
	logLevel       = log.WarnLevel
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
		log.Debug("Library index is fresh.")
		return nil
	}
	log.Debug("Library index is not fresh, attempting to build from local cache.")
	if err := s.indexFromCacheFile(); err != nil {
		log.WithFields(log.Fields{"err": err}).Warn("failed to build index from cache")
	}
	if s.libraryIndex.Alive() {
		log.Info("built a fresh library index from disk cache.")
		return nil
	} else {
		log.WithFields(log.Fields{"cacheFile": s.CacheFile}).Warn("failed to build a fresh index from local disk cache")
		log.Info("Attempting to build cache from Spotify API...")
		if err := s.indexFromSpotify(); err != nil {
			return err
		}
	}
	return nil
}

func (s *LibraryService) indexFromSpotify() error {
	index := NewSpotifyLibraryIndex()
	log.Info("Rebuilding Spotify library index...")
	trackPager, err := spClient.CurrentUsersTracks()
	if err != nil {
		return err
	}
	progressBar := pb.StartNew(trackPager.Total)
	for {
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
		progressBar.Add(trackPager.Limit)
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
		log.WithFields(log.Fields{"url": r.URL.String()}).Debug("unhandled request")
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
		log.Info("running one-off auth server...")
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
		log.Info("The current Spotify client is authenticated.")
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
		fmt.Println("Authenticated successfully with Spotify.")
		return nil
	case <-timeoutCh:
		return fmt.Errorf("Authentication timed out.")

	}
}

// HandleAuthCallback handles the Spotify OAuth2.0 callback and passes on an auth'd client
func HandleAuthCallback(w http.ResponseWriter, r *http.Request) {
	// must use the same session key here that you used to generate the URL
	token, err := auth.Token(sessionKey, r)
	if err != nil {
		log.WithFields(log.Fields{"sessionKey": sessionKey, "err": err}).Error("received auth callback, failed to retrieve token.")
		http.Error(w, fmt.Sprintf("Couldn't get token from sessionkey %s, request %v", sessionKey, r), http.StatusNotFound)
		return
	}
	// create a client using the specified token
	c := auth.NewClient(token)
	clientCh <- &c
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("200 - OK"))
	log.WithFields(log.Fields{"token": token}).Info("created client, auth flow complete")
}

// HandleCleanPotentials cleans my Potentials playlist. It removes all songs i have already saved in
// my library from the playlist.
func HandleCleanPotentials(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("200 - OK"))
	cleaned, err := cleanPotentials(false)
	if err != nil {
		log.WithFields(log.Fields{"err": err}).Error("error cleaning Potentials playlist")
		return
	}
	log.WithFields(log.Fields{"numRemoved": cleaned}).Info("successfully cleaned duplicate tracks from the Potentials playlist")
}

// cleanPotentials removes duplicate tracks from the configured spotify
// potentials playlist
func cleanPotentials(dryRun bool) (int, error) {
	// Fetch the Potentials playlist
	playlist, err := spClient.GetPlaylist(config.Spotify.PotentialsPlaylistID)
	if err != nil {
		return 0, err
	}
	log.WithFields(log.Fields{"playlistID": playlist.ID}).Info("cleaning Potentials playlist...")
	fmt.Printf("Cleaning your Potentials playlist: %s...\n", playlist.Name)

	// Clean the playlist page by page cross-referencing the library cache
	pager := &playlist.Tracks
	progressBar := pb.StartNew(pager.Total)
	duplicates := []spotify.PlaylistTrack{}
	for {
		begin := time.Now()
		duplicatesInPage, err := getDuplicates(pager.Tracks)
		if err != nil {
			return 0, err
		}
		log.WithFields(log.Fields{"duration": time.Since(begin)}).Debug("getDuplicates")
		duplicates = append(duplicates, duplicatesInPage...)
		if err = spClient.NextPage(pager); err != nil {
			if err == spotify.ErrNoMorePages {
				break
			}
			return 0, err
		}
		progressBar.Add(pager.Limit)
	}
	progressBar.Finish()
	ids := []spotify.ID{}
	for _, t := range duplicates {
		fmt.Printf("[DUPLICATE] %s\n", TrackString(t.Track))
		ids = append(ids, t.Track.ID)
	}
	if !dryRun {
		// Assuming this is atomic... the first returned value is the new playlist
		// snapshot for future requests, unused for now. When I use the snapshot
		// in the next Request I get an error from spotify: "Invalid playlist Id"
		playlistID := config.Spotify.PotentialsPlaylistID
		for {
			// Can only remove 100 tracks per request.
			toRemove, rest := FirstNIDs(ids, 100)
			//if snapshot, err := spClient.RemoveTracksFromPlaylist(playlistID, toRemove...); err != nil {
			if _, err := spClient.RemoveTracksFromPlaylist(playlistID, toRemove...); err != nil {
				return 0, err
			} else if len(rest) > 0 {
				//playlistID = spotify.ID(snapshot)
			} else { // Nothing else to remove
				break
			}
		}

	}
	return len(duplicates), nil
}

// Need to implement this because Go doesn't have generics. Returns the first n
// IDs in the list and the rest of the list
func FirstNIDs(ids []spotify.ID, n int) ([]spotify.ID, []spotify.ID) {
	if len(ids) > n {
		return ids[:n], ids[n:]
	} else {
		return ids, []spotify.ID{}
	}
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
	return fmt.Sprintf("%s, %s, on %s released %s, Track ID: %s", t.Name, artistString, t.Album.Name, t.Album.ReleaseDate, t.ID)
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

type LevelValue struct {
	Verbosity string
	Level     *log.Level
}

func (v LevelValue) String() string {
	return v.Verbosity
}

func (v LevelValue) Set(l string) error {
	if i, err := strconv.Atoi(l); err != nil {
		return err
	} else {
		if i < 0 || i > 3 {
			return log.ErrInvalidLevel
		} else {
			v.Verbosity = l
			*v.Level = log.Level(3 - i)
		}
	}
	return nil

}

func main() {

	flag.BoolVar(&runserver, "runserver", false, "runs potentials-utils in server mode")
	flag.BoolVar(&dryRun, "dry-run", false, "prints tracks that would be deleted from Potentials instead of removing them if true")
	flag.BoolVar(&noCache, "no-cache", false, "if true, invalidates your local spotify library cache and rebuilds it from scratch")
	flag.StringVar(&cfgPath, "config", "config.yaml", "path to potentials-utils config file")
	flag.Var(&LevelValue{Level: &logLevel}, "verbosity", "sets application verbosity [0-3] (default 1)")
	flag.Parse()

	log.SetLevel(logLevel)
	log.WithFields(log.Fields{"level": logLevel}).Info("logging level")

	contents, err := ioutil.ReadFile(cfgPath)
	if err != nil {
		log.WithFields(log.Fields{"path": cfgPath}).Fatal("config file not found")
	}
	err = yaml.Unmarshal(contents, &config)
	if err != nil {
		log.WithFields(log.Fields{"err": err}).Fatal("failed to unmarshal YAML config")
	}
	auth = spotify.NewAuthenticator(config.Spotify.CallbackURL, spotify.ScopeUserReadPrivate, spotify.ScopePlaylistReadPrivate, spotify.ScopePlaylistModifyPublic, spotify.ScopePlaylistModifyPrivate, spotify.ScopeUserLibraryRead)
	// Stupid library reads by default from environment variables so we have to
	// manually set credentials here.
	auth.SetAuthInfo(config.Spotify.ID, config.Spotify.Secret)
	rand.Seed(time.Now().UTC().UnixNano())

	libraryService, err = NewLibraryService(config.Cache.CacheDir)
	if err != nil {
		log.WithFields(log.Fields{"err": err}).Fatal("failed to start the potentials-utils library service")
	}
	if runserver {
		log.Info("Server UP")
		authSrv := authServer()
		authSrv.ListenAndServe()
	} else {
		if dryRun {
			fmt.Println("Running cleanPotentials in dry-run mode. No tracks will be deleted from your playlist.")
		}
		cleaned, err := cleanPotentials(dryRun)
		if err != nil {
			log.WithFields(log.Fields{"err": err}).Fatal(err.Error())
		}
		log.WithFields(log.Fields{"numRemoved": cleaned}).Info("removed tracks from potentials playlist")
		fmt.Println("Potentials playlist cleaned.")
	}

}
