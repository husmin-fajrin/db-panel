VERSION ?= dev

.PHONY: build test

build:
	CGO_ENABLED=0 go build -ldflags "-X github.com/husmin-fajrin/db-panel/internal/version.Version=$(VERSION)" -o dist/dbpanel ./cmd/dbpanel

test:
	go vet ./... && go test ./... && bash scripts/install_test.sh
