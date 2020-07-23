#!/bin/sh
export $(cat .env)

go run main.go
