# TODO

## Deployment
[ ]Long running instance deployed... somewhere? GCP?

## Features
[ ]Deduplicate real library tracks
[ ]Minimal web interface
[ ]Automatic removal of tracks that can be safely assumed i "don't like". Maybe:
  older than a year, >20 listens?
[ ]Print number of removed tracks and current length of playlist 

## Optimization
[x] Write cache to disk, read it back so that repeated one-off runs don't take so
  long
[ ] Only refresh cache if there has been a change to my spotify library instead
of a static timeout

## Bugs
[ ]Spotify auth is not working. Oauth 2.0 Request goes out to spotify, request
  fails, never hits callback URL. No obvious problems on my spotify API
  dashboard. 
