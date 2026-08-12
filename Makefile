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
CODERUNNER_IMAGE ?= $(REGISTRY)/verse-coderunner:$(TAG)   # its own image; see its Dockerfile

DIST := dist

O_PKG := github.com/signadot/tony-format/go-tony/cmd/o

# Three binaries, one image:
#   verse    the engine + CLI
#   o        the tony daemons verse spawns for logd+docd (pinned, built alongside verse)
#   sourced  the shipped Sources (git, slack, linear) — a SEPARATE process by design, so the
#            engine binary carries no connector's wire format or credential handling. Riding
#            in the same image is free; riding in the same process is not (cmd/sourced).
define go_build
	CGO_ENABLED=0 GOOS=$(1) GOARCH=$(2) $(GO) build -o $(DIST)/$(1)/$(2)/verse ./cmd/verse
	CGO_ENABLED=0 GOOS=$(1) GOARCH=$(2) $(GO) build -o $(DIST)/$(1)/$(2)/o $(O_PKG)
	CGO_ENABLED=0 GOOS=$(1) GOARCH=$(2) $(GO) build -o $(DIST)/$(1)/$(2)/sourced ./cmd/sourced
endef

.DEFAULT_GOAL := help

.PHONY: bin
bin: ## build verse + o + sourced for the host → dist/$(GOOS)/$(GOARCH)/ (dev use)
	$(call go_build,$(GOOS),$(GOARCH))

.PHONY: sourced
sourced: ## build just the source connectors (git, slack, linear) for the host
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) $(GO) build -o $(DIST)/$(GOOS)/$(GOARCH)/sourced ./cmd/sourced

.PHONY: dist-linux
dist-linux: ## cross-compile verse + o + sourced for linux/$(IMAGE_ARCH) → dist/linux/$(IMAGE_ARCH)/ (for the image)
	$(call go_build,linux,$(IMAGE_ARCH))

.PHONY: docker
docker: dist-linux ## build the image, COPYing the pre-built linux binaries (no go build in docker)
	$(DOCKER) build --platform linux/$(IMAGE_ARCH) --build-arg TARGETARCH=$(IMAGE_ARCH) -t $(IMAGE) .

# The coderunner is its OWN image and its own target. It is not folded into `docker` because the
# two images have nothing to do with each other's cadence: the runtime image is thin and pinned
# to the engine, this one is sized by the toolchain the repositories it serves need to build.
.PHONY: coderunner-linux
coderunner-linux: ## cross-compile the coderunner example for linux/$(IMAGE_ARCH)
	CGO_ENABLED=0 GOOS=linux GOARCH=$(IMAGE_ARCH) $(GO) build \
	  -o $(DIST)/linux/$(IMAGE_ARCH)/coderunner ./docs/reference/examples/coderunner

.PHONY: coderunner-docker
coderunner-docker: coderunner-linux ## build the coderunner image (bubblewrap containment)
	$(DOCKER) build --platform linux/$(IMAGE_ARCH) --build-arg TARGETARCH=$(IMAGE_ARCH) \
	  -t $(CODERUNNER_IMAGE) -f docs/reference/examples/coderunner/Dockerfile .

.PHONY: load
load: docker ## build + load the image into minikube (Docker Desktop k8s needs no load)
	minikube image load $(IMAGE)

.PHONY: push
push: docker ## build + push $(PUSH_IMAGE) to the registry
	$(DOCKER) tag $(IMAGE) $(PUSH_IMAGE)
	$(DOCKER) push $(PUSH_IMAGE)

# make deploy PROFILE=minikube                     # default SC (no mount)
# make deploy PROFILE=minikube HOSTPV=true HOSTPATH=/mnt/verse   # host-linked (needs --mount)
# make deploy PROFILE=staging                      # the profile's own registry ref
# make deploy PROFILE=staging IMAGE=example/verse-staging:v3    # …a particular build of it
# make deploy E='-e gitSource=true -e slackSource=true'          # …plus source connectors
#
# E is the escape hatch for everything the three named variables do not cover — the source
# knobs are a dozen on their own (deploy/README.md), and naming each here would make this
# target a second copy of build.tony's env block that could disagree with it.
PROFILE  ?=
HOSTPV   ?=
HOSTPATH ?=
E        ?=

# Which image the render gets, and when NOT to say. `-e` beats a profile, so passing
# `-e image=$(IMAGE)` unconditionally overrode every profile that names its own ref — which
# deployed the local dev tag to staging, where no node can pull it. So:
#
#   no PROFILE  — nothing else names one; pass $(IMAGE), the local flow's just-built tag.
#   a PROFILE   — it names its own (minikube: verse:dev, staging: example/verse-staging),
#                 so stay quiet unless this invocation named one itself.
#
# `origin` is what tells "you said IMAGE/NAME/TAG" from the `?=` defaults at the top of this
# file: those report `file`, a command-line or exported one reports otherwise. NAME/TAG count
# as naming one, but they compose into $(NAME):$(TAG) — the SHARED repository — so on staging
# `TAG=v9` renders `verse:v9` and fails to pull. Name IMAGE= outright there.
IMAGE_NAMED  := $(filter-out file default undefined,$(origin IMAGE) $(origin NAME) $(origin TAG))
DEPLOY_IMAGE := $(if $(PROFILE),$(if $(IMAGE_NAMED),$(IMAGE)),$(IMAGE))

.PHONY: deploy
deploy: ## render deploy/ and kubectl apply (PROFILE=minikube|staging; IMAGE= to pin one; HOSTPV/HOSTPATH to host-link; E='-e k=v …')
	$(O) build deploy $(if $(PROFILE),-p $(PROFILE),) $(if $(DEPLOY_IMAGE),-e image=$(DEPLOY_IMAGE),) $(if $(HOSTPV),-e hostPV=$(HOSTPV),) $(if $(HOSTPATH),-e hostPath=$(HOSTPATH),) $(E) | kubectl apply -f -

# config, launch and task carry //go:generate tony-codegen directives; their <pkg>_gen.go
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

# A diagram is the one thing in this tree nothing reads until a browser does, months later —
# mkdocs copies the block through untouched, so a syntax error is invisible at build time and
# surfaces as mermaid's error text printed where the picture should be. This parses every block
# with the mermaid the SITE loads, asking mkdocs-mermaid2-plugin which version that is.
#
# It needs node, which this repo otherwise does not, so it SKIPS loudly rather than failing
# when there is none: `make check` has to stay runnable on a machine with nothing but Go.
.PHONY: check-mermaid
check-mermaid: ## fail if a ```mermaid block does not parse (skips if node is absent)
	@./scripts/check-mermaid.sh

.PHONY: check
check: check-generate check-json check-mermaid ## every static check (what CI should run before the tests)

.PHONY: smoke
smoke: ## end-to-end smoke of the core surfaces (isolated in-memory server on an ephemeral port)
	@./smoke.sh

# Note the difference from smoke: smoke builds its own binaries and its own server and
# reaps both; this one writes into YOUR running verse and leaves it there to play with.
.PHONY: seed-demo
seed-demo: ## seed a RUNNING verse with a demo charter + entities (./scripts/seed-demo.sh --help)
	@./scripts/seed-demo.sh

# The two the tutorial drives, split so each section seeds only what it is about: a world of
# entities with nothing reacting to them, and the rules that react, one shape at a time.
.PHONY: seed-entities
seed-entities: ## seed a RUNNING verse with a world of entities and no charter (tutorial §2)
	@./scripts/seed-entities.sh

.PHONY: shapes
shapes: ## list the charter shapes (./scripts/charter-shapes.sh run <shape> to install and fire one)
	@./scripts/charter-shapes.sh

# Correlation across systems, run rather than argued: three id schemes, the derived link the
# engine names f(rule, subject), and the set read that reassembles the chain.
.PHONY: lineage
lineage: ## seed a RUNNING verse with a commit → PR → release chain (./scripts/lineage.sh --help)
	@./scripts/lineage.sh

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
