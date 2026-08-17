GO ?= go

# every target names an action, not a file — without this the build/ directory
# makes `build` (and everything depending on it) a silent no-op.
.PHONY: build test check smoke atlas nyc cities rail city dist clean-dist

build:
	$(GO) build ./...

test:
	$(GO) test ./...

# check — exactly what CI runs, in the same order. `go test` runs only a
# SUBSET of vet's analyzers, so a full `go vet ./...` catches things a
# green local `make test` does not: an httpresponse misuse got through
# that way and failed the release after the tag was already cut. Run this
# before pushing to main.
check: build
	$(GO) vet ./...
	$(GO) test ./...

# dist — the release archives, built exactly as CI builds them, so what
# you check locally is what ships (docs/RELEASING.md).
VERSION := $(shell tr -d '[:space:]' < VERSION)
TARGETS := darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64 windows/arm64

dist: clean-dist
	@for t in $(TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; \
		bin=portolan; [ "$$os" = "windows" ] && bin=portolan.exe; \
		name=portolan_$(VERSION)_$${os}_$${arch}; \
		echo "  $$os/$$arch"; \
		mkdir -p dist/stage/$$name/docs; \
		GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 $(GO) build -trimpath \
			-ldflags "-s -w -X main.version=$(VERSION)" \
			-o dist/stage/$$name/$$bin ./cmd/portolan || exit 1; \
		cp -R style dist/stage/$$name/style; \
		cp README.md LICENSE dist/stage/$$name/; \
		cp docs/CLI.md docs/CORRIDORS.md docs/CITIES.md dist/stage/$$name/docs/; \
		if [ "$$os" = "windows" ]; then \
			(cd dist/stage && zip -qr ../$$name.zip $$name); \
		else \
			tar -czf dist/$$name.tar.gz -C dist/stage $$name; \
		fi; \
	done
	@rm -rf dist/stage
	@cd dist && shasum -a 256 portolan_* > SHA256SUMS
	@echo && ls -lh dist/ | tail -n +2 | awk '{print "  " $$5 "\t" $$9}'

clean-dist:
	@rm -rf dist

# end-to-end on the committed NYC fixtures: tracks -> bundles -> score
smoke: build
	$(GO) run ./cmd/portolan chart --rail testdata/nyc-rail.geojson --out /tmp/portolan-smoke.geojson
	$(GO) run ./cmd/portolan sound --network testdata/sketches/nyc.json --build /tmp/portolan-smoke.geojson || true

# hot reload: air rebuilds+restarts on .go changes; HTML/JS assets are
# served live from disk (refresh only). Install: go install github.com/air-verse/air@latest
atlas:
	@command -v air >/dev/null 2>&1 || command -v $$HOME/go/bin/air >/dev/null 2>&1 || \
		(echo "air not found — go install github.com/air-verse/air@latest" && exit 1)
	@PATH=$$PATH:$$HOME/go/bin air

nyc: build
	$(GO) run ./cmd/portolan chart --gtfs ~/Documents/code/barrelman/data/gtfs/5.zip \
		--rail testdata/nyc-rail.geojson --out build/nyc.geojson
	$(GO) run ./cmd/portolan sound --network sketches/network-5.json --build build/nyc.geojson || true

# the other test cities (docs/CITIES.md) — every one of them drives off
# portolan.json alone, so adding a city is a config row, not a code change.
cities:                 # what's wired, and which inputs are still missing
	@tools/feed.sh list

rail:                   # OSM rail extract for one feed: make rail FEED=london
	tools/feed.sh rail $(FEED)

feed: build             # chart + score one feed: make feed FEED=london
	tools/feed.sh build $(FEED)
