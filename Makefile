BINARY=potentials-utils
TAG=$(shell git rev-parse HEAD)

build:
	go build -o bin/$(BINARY)

test:
	go test

image: build
	docker build -t potentials-utils:$(TAG) .


run: build
	chmod +x bin/$(BINARY)
	./bin/$(BINARY)
