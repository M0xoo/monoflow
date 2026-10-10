.PHONY: all build test clean

all: build test

build:
	go build -ldflags="-s -w" -o bin/monoflow ./cmd/monoflow

test:
	go test -v ./...

clean:
	rm -rf bin/monoflow
