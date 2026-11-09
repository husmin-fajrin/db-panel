VERSION ?= dev

.PHONY: build release test test-e2e screens

build:
	CGO_ENABLED=0 go build -ldflags "-X github.com/husmin-fajrin/db-panel/internal/version.Version=$(VERSION)" -o dist/dbpanel ./cmd/dbpanel

release:
	mkdir -p dist
	for arch in amd64 arm64; do \
		GOOS=linux GOARCH=$$arch CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X github.com/husmin-fajrin/db-panel/internal/version.Version=$(VERSION)" -o dist/dbpanel_linux_$$arch ./cmd/dbpanel || exit 1; \
	done
	cd dist && sha256sum dbpanel_linux_amd64 dbpanel_linux_arm64 > SHA256SUMS

test:
	go vet ./... && go test -timeout 60m ./... && bash scripts/install_test.sh

# End-to-end: a real panel, two agents and two mysqld instances (needs mysqld).
test-e2e:
	go test -timeout 30m -run TestE2E ./internal/panel/

# Screenshot tour of every page in both themes at 1440 and 390 px (a
# development tool, not a test; needs mysqld, Node with playwright and its
# Chromium). The PNG files go to SCREENS_DIR.
SCREENS_DIR ?= /tmp/dbpanel-layar
screens:
	DBPANEL_SCREENSHOT_DIR=$(SCREENS_DIR) go test -tags e2e_browser -count=1 -timeout 30m -run TestE2EScreenshotTour ./internal/panel/
