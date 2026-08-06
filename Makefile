GO ?= go

# every target names an action, not a file — without this the build/ directory
# makes `build` (and everything depending on it) a silent no-op.
.PHONY: build test smoke atlas nyc cities rail city

build:
	$(GO) build ./...

test:
	$(GO) test ./...

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
	@tools/city.sh list

rail:                   # OSM rail extract for one city: make rail CITY=london
	tools/city.sh rail $(CITY)

city: build             # chart + score one city: make city CITY=london
	tools/city.sh build $(CITY)
