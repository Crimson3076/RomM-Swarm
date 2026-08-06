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

.PHONY: clean
clean:
	rm -rf $(BIN) probe-out
