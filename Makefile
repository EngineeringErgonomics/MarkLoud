GOFILES := $(shell find . -name '*.go' -not -path './vendor/*' -not -path './.git/*')

.PHONY: fmt fmtcheck vet lint test test-race tidy coverage ci build

fmt:
	gofmt -w $(GOFILES)

fmtcheck:
	@files=$$(gofmt -l $(GOFILES)); \
	if [ -n "$$files" ]; then \
		echo "gofmt needed on:"; echo "$$files"; exit 1; \
	fi

vet:
	go vet ./...

lint:
	staticcheck ./...

test:
	go test ./...

test-race:
	go test -race ./...

coverage:
	go test -coverprofile=coverage.out ./...

tidy:
	go mod tidy

ci: fmtcheck vet lint test

build:
	go build -o markloud ./cmd/markloud

goreleaser-check:
	goreleaser check

goreleaser-snapshot:
	goreleaser release --clean --snapshot
