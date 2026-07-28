SHELL := /usr/bin/env bash -o pipefail
.SHELLFLAGS := -ec

# Build the Go binaries LOCALLY (fast, uses your host build cache) and let `docker build`
# only COPY them in — no Go toolchain or `go build` inside Docker. Every dev stage has a
# target: `make bin` (host binaries), `make dist-linux` (image binaries), `make docker`,
# `make load`, `make push`, `make deploy`.

GO      ?= go
GOOS    ?= $(shell $(GO) env GOOS)
GOARCH  ?= $(shell $(GO) env GOARCH)
O       ?= o          # the tony `o` CLI on PATH (for `make deploy`)
DOCKER  ?= docker

# Image. The container runs linux; IMAGE_ARCH is the node arch (Docker Desktop on Apple
# Silicon = arm64; most EKS = amd64 — override: make docker IMAGE_ARCH=amd64).
REGISTRY  ?= signadot
NAME      ?= verse
TAG       ?= dev
IMAGE     ?= $(NAME):$(TAG)                 # local dev tag (docker-desktop/minikube)
PUSH_IMAGE?= $(REGISTRY)/$(NAME):$(TAG)     # registry ref for `make push`
IMAGE_ARCH?= $(GOARCH)

DIST := dist

O_PKG := github.com/signadot/tony-format/go-tony/cmd/o

# The o binary verse spawns for logd+docd (built alongside verse).
define go_build
	CGO_ENABLED=0 GOOS=$(1) GOARCH=$(2) $(GO) build -o $(DIST)/$(1)/$(2)/verse ./cmd/verse
	CGO_ENABLED=0 GOOS=$(1) GOARCH=$(2) $(GO) build -o $(DIST)/$(1)/$(2)/o $(O_PKG)
endef

.DEFAULT_GOAL := help

.PHONY: bin
bin: ## build verse + o for the host → dist/$(GOOS)/$(GOARCH)/ (dev use)
	$(call go_build,$(GOOS),$(GOARCH))

.PHONY: dist-linux
dist-linux: ## cross-compile verse + o for linux/$(IMAGE_ARCH) → dist/linux/$(IMAGE_ARCH)/ (for the image)
	$(call go_build,linux,$(IMAGE_ARCH))

.PHONY: docker
docker: dist-linux ## build the image, COPYing the pre-built linux binaries (no go build in docker)
	$(DOCKER) build --platform linux/$(IMAGE_ARCH) --build-arg TARGETARCH=$(IMAGE_ARCH) -t $(IMAGE) .

.PHONY: load
load: docker ## build + load the image into minikube (Docker Desktop k8s needs no load)
	minikube image load $(IMAGE)

.PHONY: push
push: docker ## build + push $(PUSH_IMAGE) to the registry
	$(DOCKER) tag $(IMAGE) $(PUSH_IMAGE)
	$(DOCKER) push $(PUSH_IMAGE)

# make deploy PROFILE=minikube                     # default SC (no mount)
# make deploy PROFILE=minikube HOSTPV=true HOSTPATH=/mnt/verse   # host-linked (needs --mount)
PROFILE  ?=
HOSTPV   ?=
HOSTPATH ?=
.PHONY: deploy
deploy: ## render deploy/ and kubectl apply (PROFILE=minikube|staging; HOSTPV=true HOSTPATH=… to host-link)
	$(O) build deploy $(if $(PROFILE),-p $(PROFILE),) -e image=$(IMAGE) $(if $(HOSTPV),-e hostPV=$(HOSTPV),) $(if $(HOSTPATH),-e hostPath=$(HOSTPATH),) | kubectl apply -f -

# config, launch and process carry //go:generate tony-codegen directives; their <pkg>_gen.go
# and schema_gen.tony are COMMITTED, and check-generate is the guard that they are current.
#
# Why generated rather than reflected, now that gomap's reflection path is correct on its
# own (go-tony v0.0.89): an unsupported field type becomes a `make generate` failure that CI
# catches, instead of a marshal error on a live request. That matters most for exactly these
# types, which carry the HTTP wire once it switches over.
#
# Package trigger is deliberately absent: its charter grammar (Formula, Match, Requirement,
# Condition) gets hand-written codecs for the tony tag surface, and codegen picks those up
# via codec=custom — so it joins once those exist, not before.
#
# Codegen is deliberately NOT a dependency of `bin`/`dist-linux`: keeping it out of the
# build path is what lets the Dockerfile just COPY binaries.
.PHONY: generate
generate: ## regenerate tony codegen (currently a no-op — see the note above)
	$(GO) generate ./...

# Scoped to the generated artifacts on purpose: a bare `git diff --exit-code` fails on any
# unrelated edit in the tree, which makes the target useless anywhere but a clean CI
# checkout — and a check you cannot run locally is a check that fails for the first time in
# CI. The second clause catches a NEW generated file that was never committed, which a diff
# alone would not see.
GEN_FILES := '*_gen.go' '*schema_gen.tony'
.PHONY: check-generate
check-generate: ## fail if the generated files are stale or uncommitted
	$(GO) generate ./...
	@git diff --exit-code -- $(GEN_FILES) || \
	  { echo "generated files are stale — run 'make generate' and commit the result"; exit 1; }
	@test -z "$$(git ls-files --others --exclude-standard -- $(GEN_FILES))" || \
	  { echo "generated files are not committed:"; \
	    git ls-files --others --exclude-standard -- $(GEN_FILES); exit 1; }
	@echo "generated files are current"

.PHONY: test
test: ## go test ./...
	$(GO) test ./...

# Tests whose RUNTIME is the point — waiting out a real timeout to show what happens when it
# expires — are skipped by default via requireLong(), because one of them was ~90% of the
# suite's wall time. They are not exercised on an ordinary run; this is how to run them.
.PHONY: test-long
test-long: ## go test ./... including the long, timeout-waiting tests (GOTEST_LONG=1)
	GOTEST_LONG=1 $(GO) test ./...

# verse renders through package codec; a stray encoding/json is how that quietly stops
# being true. It does not fail loudly — it produces the wrong dialect, or fails to parse one
# and looks like an empty stream. Both happened during the migration.
.PHONY: check-json
check-json: ## fail if non-test code imports encoding/json outside the allowlist
	@./scripts/check-json.sh

.PHONY: check
check: check-generate check-json ## every static check (what CI should run before the tests)

.PHONY: smoke
smoke: ## end-to-end smoke of the core surfaces (isolated in-memory server on a free port)
	@./smoke.sh

# Note the difference from smoke: smoke builds its own binaries and its own server and
# reaps both; this one writes into YOUR running verse and leaves it there to play with.
.PHONY: seed-demo
seed-demo: ## seed a RUNNING verse with a demo charter + entities (./seed-demo.sh --help)
	@./seed-demo.sh

# The representation is a DEFAULT, not a fixture: tony out of the box, JSON when asked for.
# Running the suite only one way would let the other rot silently — and the JSON path is what
# every existing client and `curl | jq` still uses. This is the target CI should call.
.PHONY: smoke-all
smoke-all: ## smoke under both renderings (tony default, then VERSE_OUTPUT_FORMAT=yaml)
	@./smoke.sh
	@VERSE_OUTPUT_FORMAT=yaml ./smoke.sh

.PHONY: clean
clean: ## remove dist/
	rm -rf $(DIST)

.PHONY: help
help: ## list targets
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | sort | \
	  awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'
