# Bernard build.
#
#   make            build everything for this machine
#   make build-nuc  cross-compile for the NUC (linux/amd64, static)
#   make dev        run the server plus the Vite dev server
#   make deploy     ship the binary to the NUC and restart it

GO      ?= go
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.Version=$(VERSION)
NUC     ?= kiosk@bernard.local

.PHONY: all portal build build-nuc dev run test vet fmt clean deploy

all: build

## portal — build the React app that gets embedded into the binary
portal:
	cd web/portal && npm ci --silent && npm run build

## build — server for this machine (portal must be built first)
build: portal
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/bernard ./cmd/bernard
	@echo "built bin/bernard $(VERSION)"

## build-nuc — static linux/amd64 binary to scp to the NUC.
## The SQLite driver is pure Go, so this needs no cross-compiler and the result
## has no libc dependency to mismatch against the NUC's Ubuntu version.
build-nuc: portal
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
		$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/bernard ./cmd/bernard
	@echo "built bin/bernard $(VERSION) for linux/amd64"

## dev — server on :8080 and the Vite dev server on :5173 (use :5173)
dev:
	@echo "portal: http://localhost:5173   display: http://localhost:8080/display"
	@BERNARD_ADDR=:8080 \
	 BERNARD_DATA_DIR=$$PWD/.devdata \
	 BERNARD_DEV_PASSWORD=$${BERNARD_DEV_PASSWORD:-dev} \
	 $(GO) run ./cmd/bernard & \
	 cd web/portal && npm run dev; \
	 kill %1 2>/dev/null || true

## run — the built binary against a local data dir
run: build
	BERNARD_DATA_DIR=$$PWD/.devdata BERNARD_DEV_PASSWORD=dev ./bin/bernard

test:
	CGO_ENABLED=0 $(GO) test ./...

vet:
	CGO_ENABLED=0 $(GO) vet ./...

fmt:
	$(GO) fmt ./...

clean:
	rm -rf bin web/portal/dist .devdata

## deploy — copy the binary up and restart. Config and data stay put.
deploy: build-nuc
	scp bin/bernard $(NUC):/tmp/bernard
	ssh $(NUC) 'sudo install -o root -g root -m 755 /tmp/bernard /usr/local/bin/bernard \
		&& rm /tmp/bernard \
		&& sudo systemctl restart bernard \
		&& sleep 1 && systemctl is-active bernard'
