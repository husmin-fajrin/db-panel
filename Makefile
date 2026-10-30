VERSION ?= dev

.PHONY: build release test

build:
	CGO_ENABLED=0 go build -ldflags "-X github.com/husmin-fajrin/db-panel/internal/version.Version=$(VERSION)" -o dist/dbpanel ./cmd/dbpanel

release:
	mkdir -p dist
	for arch in amd64 arm64; do \
		GOOS=linux GOARCH=$$arch CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X github.com/husmin-fajrin/db-panel/internal/version.Version=$(VERSION)" -o dist/dbpanel_linux_$$arch ./cmd/dbpanel || exit 1; \
	done
	cd dist && sha256sum dbpanel_linux_amd64 dbpanel_linux_arm64 > SHA256SUMS

test:
	go vet ./... && go test ./... && bash scripts/install_test.sh
