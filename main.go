// Author: paulangton
// Copyright 2020

package main

import (
	"fmt"
    "flag"
	"log"
	"math/rand"
	"net/http"
	"os"
    "context"
	"time"

	"github.com/zmb3/spotify"
    "potentials-utils/prefixtree"
)

var runserver bool
var dryRun bool

var (
	clientCh             = make(chan *spotify.Client)
	auth                 = spotify.NewAuthenticator(os.Getenv("SPOTIFY_CALLBACK_URL"), spotify.ScopeUserReadPrivate, spotify.ScopePlaylistReadPrivate, spotify.ScopePlaylistModifyPublic, spotify.ScopePlaylistModifyPrivate, spotify.ScopeUserLibraryRead)
	potentialsPlaylistID = spotify.ID(os.Getenv("SPOTIFY_POTENTIALS_PLAYLIST_ID"))

	spClient     *spotify.Client
	libraryCache *SpotifyLibraryCache
	sessionKey   string
)

// SpotifyLibraryCache represents an in-memory cache of the current users' spotify library. It must
// be completely rebuilt if the current time is after the evictionTime.
type SpotifyLibraryCache struct {
	items    map[spotify.ID]*spotify.SavedTrack
    prefixTree *prefixtree.PrefixTree
	lifetime time.Duration
	// This cache has to be completely rebuilt, no element-wise evictions
	evictionTime time.Time
}

// NewSpotifyLibraryCache creates a SpotifyLibraryCache with a lifetime of 1 day.
func NewSpotifyLibraryCache() (*SpotifyLibraryCache, error) {
	err := AuthMe()
	if err != nil {
		return nil, err
	}
	c := &SpotifyLibraryCache{
		items:        map[spotify.ID]*spotify.SavedTrack{},
        prefixTree:   prefixtree.NewPrefixTree(),
		lifetime:     24 * time.Hour,
		evictionTime: time.Now(),
	}
	err = c.readyCache()
	if err != nil {
		return nil, err
	}
	return c, nil

}

// Get returns the corresponding SavedTRack for the provided key if it exists and the cache is
// fresh. Will rebuild the cache if stale.
func (c *SpotifyLibraryCache) Get(k spotify.ID) (*spotify.SavedTrack, error) {
	err := c.readyCache()
	if err != nil {
		return nil, err
	}
	v := c.items[k]
	return v, nil
}

// Put adds the provided key-value pair (ID -> SavedTrack) to the cache
func (c *SpotifyLibraryCache) Put(k spotify.ID, v spotify.SavedTrack) error {
	err := c.readyCache()
	if err != nil {
		return err
	}
	c.items[k] = &v
	return nil
}

func (c *SpotifyLibraryCache) readyCache() error {
	if time.Now().Before(c.evictionTime) {
		// Cache has not expired yet, cache ready
		return nil
	}

	// Cache has expired, need to rebuild
	log.Printf("Rebuilding Spotify library cache index...")
	trackPager, err := spClient.CurrentUsersTracks()
	if err != nil {
		return err
	}
	for {
        log.Printf("Built %d/%d tracks...", trackPager.Offset, trackPager.Total)
		for _, t := range trackPager.Tracks {
			c.items[t.ID] = &t
		}
		err := spClient.NextPage(trackPager)
		if err != nil {
			if err != spotify.ErrNoMorePages {
				return err
			}
			break
		}
	}
	c.evictionTime = time.Now().Add(c.lifetime)
	log.Printf("Successfully built library cache of %d tracks.", len(c.items))
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
        Addr: ":8080",
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
        ctx, cancelFunc := context.WithTimeout(context.Background(), time.Minute)
        defer cancelFunc()
        defer authSrv.Shutdown(ctx)
        err := authMeWithTimeout(time.Minute)
		if err != nil {
			return err
		}

    }
	if spClient == nil {
		err := authMeWithTimeout(time.Minute)
		if err != nil {
			return err
		}
	}
	if _, err := spClient.CurrentUser(); err != nil {
		err = authMeWithTimeout(time.Minute)
		if err != nil {
			return err
		}
		// The current client works, just use it.
		log.Printf("The current Spotify client is authenticated.")
	}
	return nil
}

func authMeWithTimeout(timeout time.Duration) error {
	// problems if there is ever more than one auth request in flight
	sessionKey = fmt.Sprintf("potentials-session-key-%d", rand.Intn(10000))
	url := auth.AuthURL(sessionKey)
	fmt.Printf("Visit %s in a browser to complete the authentication process.\n", url)
	timeoutCh := make(chan bool)
	go func() {
		time.Sleep(timeout)
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
		http.Error(w, "Couldn't get token", http.StatusNotFound)
		return
	}
	// create a client using the specified token
	c := auth.NewClient(token)
	clientCh <- &c
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("200 - OK"))
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

func refreshLibraryCache() error {
	if libraryCache != nil {
		// Quick cache health check, will rebuild the cache if it has expired
		libraryCache.Get(spotify.ID(""))
		return nil
	}
	c, err := NewSpotifyLibraryCache()
	if err != nil {
		return err
	}
	libraryCache = c
	return nil
}

func cleanPotentials(dryRun bool) (int, error) {
	// First build a cache of my library
	err := refreshLibraryCache()
	if err != nil {
		return 0, err
	}

	// Fetch the potentials playlist
	playlist, err := spClient.GetPlaylist(potentialsPlaylistID)
	if err != nil {
		return 0, err
	}

	// Clean the playlist page by page cross-referencing the library cache
	pager := &playlist.Tracks
	numCleaned := 0
	for {
		_, numCleanedForPage, err := cleanPotentialsPage(pager.Tracks, potentialsPlaylistID, dryRun)
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

// 
func TrackString(t spotify.FullTrack) string {
    artistString := ""
    for ix, a := range t.Artists {
        if ix == len(t.Artists) {
            artistString += fmt.Sprintf("%s", a.Name)
        } else {
            artistString += fmt.Sprintf("%s, ", a.Name)
        }
    }
    return fmt.Sprintf("%s, %s, on %s released %s, Track ID: %s\n",  t.Name, artistString, t.Album.Name, t.Album.ReleaseDate, t.ID)
}


// cleanPotentialsPage also returns the number of duplicate tracks cleaned on the given page
func cleanPotentialsPage(page []spotify.PlaylistTrack, playlistID spotify.ID, dryRun bool) (spotify.ID, int, error) {
	duplicateTracks := []spotify.PlaylistTrack{}
	for _, playlistTrack := range page {
		trackID := playlistTrack.Track.ID
		libraryTrack, err := libraryCache.Get(trackID)
		if err != nil {
			return spotify.ID(""), 0, err
		}
		if libraryTrack != nil {
			// track is already in our library, remove it
			// log.Printf("Found a duplicate track in the potentials playlist: %s by %s off the album %s (ID: %s).", track.Track.Name, track.Track.Artists[0].Name, track.Track.Album.Name, trackID)
			duplicateTracks = append(duplicateTracks, playlistTrack)
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

    flag.BoolVar(&runserver, "runserver", false, "runs cleanpotentials in server mode")
    flag.BoolVar(&dryRun, "dry-run", false, "prints tracks that would be deleted from potentials instead of removing them if true")
    flag.Parse()

    if runserver {
        rand.Seed(time.Now().Unix())
        log.Printf("Server UP")
        authSrv := authServer()
        log.Fatal(authSrv.ListenAndServe())
    } else {
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
