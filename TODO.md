# TODO

## Deployment
- Long running instance deployed... somewhere? GCP?

## Features
- Deduplicate real library tracks
- Minimal web interface

## Optimization
- Write cache to disk, read it back so that repeated one-off runs don't take so
  long

## Bugs
- Spotify auth is not working. Oauth 2.0 Request goes out to spotify, request
  fails, never hits callback URL. No obvious problems on my spotify API
  dashboard. 
