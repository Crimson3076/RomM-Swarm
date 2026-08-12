GO      ?= go
BIN     := bin
PKGS    := ./...

.PHONY: all
all: fmt-check vet test

.PHONY: build
build:
	$(GO) build -o $(BIN)/swarm-probe ./cmd/swarm-probe
	$(GO) build -o $(BIN)/swarm-verify ./cmd/swarm-verify
	$(GO) build -o $(BIN)/swarm-fixtures ./cmd/swarm-fixtures
	$(GO) build -o $(BIN)/swarm-bridge ./cmd/swarm-bridge
	$(GO) build -o $(BIN)/bridge ./cmd/bridge
	$(GO) build -o $(BIN)/host ./cmd/host

# Generate a sample library and catalogue, then classify it. Exercises the whole
# verification pipeline by hand without touching a real collection.
.PHONY: demo
demo: build
	$(BIN)/swarm-fixtures -out $(BIN)/sample
	@echo
	$(BIN)/swarm-verify -dat $(BIN)/sample/catalogue.dat -platform gb $(BIN)/sample/library

.PHONY: test
test:
	$(GO) test $(PKGS)

.PHONY: test-v
test-v:
	$(GO) test -v $(PKGS)

.PHONY: vet
vet:
	$(GO) vet $(PKGS)

.PHONY: fmt
fmt:
	$(GO) fmt $(PKGS)

.PHONY: fmt-check
fmt-check:
	@unformatted=$$(gofmt -l . | grep -v '^vendor/' || true); \
	if [ -n "$$unformatted" ]; then \
		echo "These files are not gofmt-clean:"; echo "$$unformatted"; exit 1; \
	fi

# Phase 0 evidence. Runs the whole acceptance harness and writes a report that
# can be attached to the Phase 0 go/stop review.
.PHONY: evidence
evidence:
	$(GO) test -run TestPhase0 -v $(PKGS)

# Probe a live RomM instance. Requires ROMM_URL and ROMM_TOKEN in the
# environment; neither is ever written into the recorded bundle.
.PHONY: probe
probe: build
	@test -n "$$ROMM_URL"   || { echo "ROMM_URL is not set";   exit 1; }
	@test -n "$$ROMM_TOKEN" || { echo "ROMM_TOKEN is not set"; exit 1; }
	$(BIN)/swarm-probe -out probe-out

.PHONY: docker-build
docker-build:
	docker build -t romm-swarm-bridge:dev .

# Local click-around testing: a throwaway named volume for /data, port 8080
# published, foreground so Ctrl-C stops it. Set ROMM_URL/ROMM_TOKEN in the
# environment to seed a first connection, or leave them unset and use the
# admin UI's /setup flow instead.
.PHONY: docker-run
docker-run: docker-build
	docker run --rm -it \
		-p 8080:8080 \
		-v romm-swarm-bridge-dev-data:/data \
		-e ROMM_URL \
		-e ROMM_TOKEN \
		romm-swarm-bridge:dev

.PHONY: docker-build-host
docker-build-host:
	docker build -f Dockerfile.host -t romm-swarm-host:dev .

# Local click-around testing for the Host: needs Postgres too, so this uses
# docker-compose rather than a bare `docker run` (unlike docker-run above,
# which doesn't need any other service). Ctrl-C stops both.
.PHONY: docker-run-host
docker-run-host:
	docker compose up --build host postgres

# Rebuilds bridge and host from the current source and (re)starts every
# long-running service in one shot — the one command to run after a
# `git pull` so a stale image is never the reason something looks broken.
# Detached (-d): unlike docker-run/docker-run-host, this doesn't tie up the
# terminal. The test service is excluded (profiles: tools in
# docker-compose.yml) — run `make docker-test` for that.
.PHONY: docker-update
docker-update:
	docker compose up -d --build

# Runs the whole Go test suite, including the Postgres-gated host/... tests,
# in containers — no local Go toolchain or Postgres install required.
.PHONY: docker-test
docker-test:
	docker compose run --rm test

.PHONY: clean
clean:
	rm -rf $(BIN) probe-out
