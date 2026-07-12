GO ?= go

build:
	$(GO) build ./...

test:
	$(GO) test ./...

# end-to-end on the committed NYC fixtures: tracks -> bundles -> score
smoke: build
	$(GO) run ./cmd/portolan chart --rail testdata/nyc-rail.geojson --out /tmp/portolan-smoke.geojson
	$(GO) run ./cmd/portolan sound --network testdata/sketches/nyc.json --build /tmp/portolan-smoke.geojson || true

atlas:
	$(GO) run ./cmd/portolan atlas --sketches ./sketches

nyc: build
	$(GO) run ./cmd/portolan chart --gtfs ~/Documents/code/barrelman/data/gtfs/5.zip \
		--rail testdata/nyc-rail.geojson --out build/nyc.geojson
	$(GO) run ./cmd/portolan sound --network sketches/network-5.json --build build/nyc.geojson || true
