# forgerun build. `go run ./cmd/forgerun serve` works without any of this
# (it serves a placeholder instead of the web UI); `make build` makes the
# full binary with the UI embedded.

.PHONY: build web test dev-web

build: web
	go build -tags webui -o forgerun ./cmd/forgerun

web:
	cd web && npm ci && npm run build

test:
	go test ./...
	cd web && npm test

# The UI with live reload on http://localhost:5173; API calls go to a
# forgerun serve running on :8080.
dev-web:
	cd web && npm run dev
