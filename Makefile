VERSION ?= dev

.PHONY: build release test test-e2e

build:
	CGO_ENABLED=0 go build -ldflags "-X github.com/husmin-fajrin/db-panel/internal/version.Version=$(VERSION)" -o dist/dbpanel ./cmd/dbpanel

release:
	mkdir -p dist
	for arch in amd64 arm64; do \
		GOOS=linux GOARCH=$$arch CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X github.com/husmin-fajrin/db-panel/internal/version.Version=$(VERSION)" -o dist/dbpanel_linux_$$arch ./cmd/dbpanel || exit 1; \
	done
	cd dist && sha256sum dbpanel_linux_amd64 dbpanel_linux_arm64 > SHA256SUMS

test:
	go vet ./... && go test -timeout 30m ./... && bash scripts/install_test.sh

# End-to-end: a real panel, two agents and two mysqld instances (needs mysqld).
test-e2e:
	go test -timeout 30m -run TestE2E ./internal/panel/
