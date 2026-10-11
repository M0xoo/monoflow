.PHONY: all build test clean e2e e2e-events

all: build test

build:
	go build -ldflags="-s -w" -o bin/monoflow ./cmd/monoflow

test:
	go test -v ./...

e2e: build
	./bin/monoflow scenario run

# Events from PRs, remote CI and MonoFlow's loops, delivered through the Claude Code hooks,
# the opencode plugin and events unread/wait (real repos, fake gh; nothing leaves the machine).
e2e-events: build
	./e2e/events/run.sh ./bin/monoflow

clean:
	rm -rf bin/monoflow
