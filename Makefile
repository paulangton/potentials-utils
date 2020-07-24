BINARY_PATH=bin/potentials-utils
TAG=$(shell git rev-parse HEAD)

build:
	go build -o bin/$(BINARY_PATH)

test:
	go test

image: build
	docker build -t potentials-utils:$(TAG) .

clean:
	rm -rf $(BINARY_PATH)

run: build
	chmod +x $(BINARY_PATH)
	./$(BINARY_PATH)
