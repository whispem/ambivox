CARGO   ?= cargo
GO      ?= go
CMAKE   ?= cmake

BACKEND ?= sim
MODELS  ?= models
LISTEN  ?= 127.0.0.1:8080
SOCKET  ?= /tmp/ambivox-media.sock

MEDIA_BIN  := $(CURDIR)/target/release/ambivox-media
SERVER_BIN := $(CURDIR)/bin/ambivox
FEATURES   := $(if $(filter sussurro,$(BACKEND)),--features sussurro,)
MODEL_ARGS := $(if $(filter sussurro,$(BACKEND)),--models $(MODELS),)

# The bundled libopus build predates CMake 4's minimum policy version.
export CMAKE_POLICY_VERSION_MINIMUM ?= 3.5

.PHONY: all build media server test test-engine test-rust test-go e2e lint check run browser-check clean

all: build

build: media server

media:
	$(CARGO) build --release -p ambivox-media $(FEATURES)

server:
	cd server && $(GO) build -trimpath -o $(SERVER_BIN) ./cmd/ambivox

test: test-engine test-rust test-go

test-engine:
	$(CMAKE) -S engine -B build/engine -DCMAKE_BUILD_TYPE=Release
	$(CMAKE) --build build/engine --parallel
	ctest --test-dir build/engine --output-on-failure

test-rust:
	$(CARGO) test --workspace
	$(CARGO) test -p ambivox-dsp --no-default-features

test-go:
	cd server && $(GO) vet -tags e2e ./... && $(GO) test -race ./...

e2e: media
	cd server && AMBIVOX_MEDIA_BIN=$(MEDIA_BIN) $(GO) test -tags e2e -count=1 ./e2e/

lint:
	$(CARGO) fmt --all --check
	$(CARGO) clippy --workspace --all-targets -- -D warnings
	@unformatted="$$(gofmt -l server)"; if [ -n "$$unformatted" ]; then echo "gofmt needed:"; echo "$$unformatted"; exit 1; fi

check: lint test e2e
	@echo "ambivox: all checks passed"

run: build
	@trap 'kill 0' INT TERM EXIT; \
	$(MEDIA_BIN) serve --socket $(SOCKET) --backend $(BACKEND) $(MODEL_ARGS) & \
	$(SERVER_BIN) -listen $(LISTEN) -media-socket $(SOCKET) & \
	wait

browser-check: build
	cd scripts/browser-check && npm install --no-audit --no-fund && \
	MEDIA_BIN=$(MEDIA_BIN) SERVER_BIN=$(SERVER_BIN) node check.mjs

clean:
	$(CARGO) clean
	rm -rf build bin scripts/browser-check/node_modules
