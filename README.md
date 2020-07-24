# potentials-utils
A small tool for managing your Spotify Potentials playlist.

## The Potentials playlist

The Potentials playlist is for all the music you think you might like,
but hasn't yet made the cut to be a full member of your library. It's what you listen to when you're
in the mood to explore new music but you don't want your ears assaulted by Discover Weekly. It's
where you put all those music recommendations you get from family, friends, coworkers, etc.

Spotify does not have great tooling for supporting this pattern, thus `potentials-utils` came to be.

## What does this actually solve

### Duplicate tracks
1. Sometimes you'll hear a song you like. You'll add it to your library and then get curious about
   the album that song is from. So you click through and see a 14 song album. You dont have time for
   that or you're not in the mood, so you add that album to your Potentials to listen to the tracks
   later BUT you just added that one song in the album to your library, so adding the whole album
   adds a track that really isnt a "potential", it's already been promoted to full library status.
   This creates a duplicate.

1. As you make your way through the Potentials, you're going to find stuff you like, so you add it
   to your library BUT there is not a single chance you're going to click through a menu after
   adding the song to your library to remove the song from Potentials. That track is now a
   duplicate.

The problem here is obvious: with the existence of duplicates, you will occasionally waste your time hitting tracks in Potentials that are not "potentials", they are songs you've already decided you like.

`potentials-utils` takes care of this by automatically removing duplicate tracks from your
Potentials playlist.

## I'd like to use this

### Deploying your own potentials-utils
1. Clone this repository
```
git clone git@github.com:paulangton/potentials-utils.git && cd potentials-utils
```
1. Register a [Spotify app](https://developer.spotify.com/dashboard/applications).
1. Install [docker](https://docs.docker.com/get-docker/). 
1. Create your own `.env.` file in this project directory with the following variables:
```
SPOTIFY_ID=<Your Spotify Client ID>
SPOTIFY_SECRET=<Your Spotify Secret>
SPOTIFY_CALLBACK_URL=http://localhost:8080/callback/spotify
SPOTIFY_USER=<Your Spotify Username>
SPOTIFY_POTENTIALS_PLAYLIST_ID=<Your Potentials Playlist ID>
```
1. Build a docker image with your new `.env` file with
```
make image
```
1. Run your container exposed on port `8080` with
```
docker run -it -p 8080:8080
```
and follow the instructions to authenticate with Spotify.

### Cleaning your Potentials
1. Kick off a cleaning of your Potentials with 
```
curl localhost:8080/spotify/cleanpotentials
```


