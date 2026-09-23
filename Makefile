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
#   iod  the shipped Sources (git, slack, linear) — a SEPARATE process by design, so the
#            engine binary carries no connector's wire format or credential handling. Riding
#            in the same image is free; riding in the same process is not (cmd/iod).
define go_build
	CGO_ENABLED=0 GOOS=$(1) GOARCH=$(2) $(GO) build -o $(DIST)/$(1)/$(2)/verse ./cmd/verse
	CGO_ENABLED=0 GOOS=$(1) GOARCH=$(2) $(GO) build -o $(DIST)/$(1)/$(2)/o $(O_PKG)
	CGO_ENABLED=0 GOOS=$(1) GOARCH=$(2) $(GO) build -o $(DIST)/$(1)/$(2)/iod ./cmd/iod
endef

.DEFAULT_GOAL := help

.PHONY: bin
bin: ## build verse + o + iod for the host → dist/$(GOOS)/$(GOARCH)/ (dev use)
	$(call go_build,$(GOOS),$(GOARCH))

.PHONY: iod
iod: ## build just the source connectors (git, slack, linear) for the host
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) $(GO) build -o $(DIST)/$(GOOS)/$(GOARCH)/iod ./cmd/iod

.PHONY: dist-linux
dist-linux: ## cross-compile verse + o + iod for linux/$(IMAGE_ARCH) → dist/linux/$(IMAGE_ARCH)/ (for the image)
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

# No CODERUNNER_PUSH_IMAGE to pair with PUSH_IMAGE: CODERUNNER_IMAGE already carries the
# registry, so there is nothing to retag. Override it whole — including into the same
# repository as the runtime image under a distinguishing tag, which is one repository to
# create and one pull credential to grant:
#
#   make coderunner-push IMAGE_ARCH=amd64 CODERUNNER_IMAGE=example/verse-staging:coderunner
.PHONY: coderunner-push
coderunner-push: coderunner-docker ## build + push $(CODERUNNER_IMAGE)
	$(DOCKER) push $(CODERUNNER_IMAGE)

.PHONY: load
load: docker ## build + load the image into minikube (Docker Desktop k8s needs no load)
	minikube image load $(IMAGE)

.PHONY: push
push: docker ## build + push $(PUSH_IMAGE) to the registry
	$(DOCKER) tag $(IMAGE) $(PUSH_IMAGE)
	$(DOCKER) push $(PUSH_IMAGE)

# STAGING, BY IMMUTABLE REF. `make ship` builds both images tagged with the COMMIT, pushes them,
# and deploys that ref — so "which build is staging running" is answered by reading the tag
# instead of comparing registry digests by hand, which is what a moving tag costs the first time
# a rollout reports success and runs the binary from before it (deploy/profiles/staging.tony).
#
# It refuses a dirty tree, because the whole value here is that the tag names a commit somebody
# can check out. A tag that says 8f2c1a4 over a build that was 8f2c1a4-plus-whatever-was-open is
# worse than `latest`: it is a moving tag wearing an immutable one's clothes.
STAGING_SHA ?= $(shell git rev-parse --short HEAD)

.PHONY: ship
ship: ## build+push both images at the current commit and deploy staging at that ref
	@if [ -n "$$(git status --porcelain)" ]; then \
	  echo "ship: the tree is dirty — the image tag would name a commit that is not what is built"; \
	  git status --short; exit 1; \
	fi
	@echo "ship: staging → $(STAGING_SHA)"
	$(MAKE) push IMAGE_ARCH=amd64 PUSH_IMAGE=$(REGISTRY)/verse-staging:$(STAGING_SHA)
	$(MAKE) coderunner-push IMAGE_ARCH=amd64 CODERUNNER_IMAGE=$(REGISTRY)/verse-coderunner:$(STAGING_SHA)
	$(MAKE) deploy PROFILE=staging IMAGE=$(REGISTRY)/verse-staging:$(STAGING_SHA) \
	  E="-e coderunnerImage=$(REGISTRY)/verse-coderunner:$(STAGING_SHA)"

# ONE TIER AT A TIME, because `ship` above applies all of them in a single kubectl apply and
# the tiers do not always tolerate rolling together. versed and docd speak logd's session
# protocol over the wire, so a protocol change (v0.0.173's match shape, say) means the two
# vintages must not meet: a client of one against a server of the other has its path read as
# "", which is the ROOT — an answer, with a commit, not an error. One apply starts both rolling
# and there is no point in it where you can say which is up.
#
# So each of these pushes the image its tier runs, applies ONLY that tier's objects, and does
# not return until they are rolled out. A protocol-changing ship is then a sequence somebody
# can read:
#
#   kubectl --context <ctx> -n verse scale deploy/versed --replicas=0
#   make ship-docd && make ship-versed && make ship-ios && make ship-coderunner
#
# The tier is the `tier` label every object in deploy/ carries, which is what `-l` selects on:
# docd (the store), versed (the engine and its two Services), source (the connectors), runner
# (the coderunner, its own image). An object with no `tier` — the Namespace, the ServiceAccount,
# the ExternalSecrets — belongs to none of them and is shipped by a full `make deploy`; these
# targets update a deployment that already exists rather than standing one up.
SHIP_IMAGE      = $(REGISTRY)/verse-staging:$(STAGING_SHA)
SHIP_CODERUNNER = $(REGISTRY)/verse-coderunner:$(STAGING_SHA)

# ship-clean is `ship`'s dirty-tree refusal, factored out so each tier inherits it: the tag
# names a commit somebody can check out, and that is no less true when shipping one tier.
.PHONY: ship-clean
ship-clean:
	@if [ -n "$$(git status --porcelain)" ]; then \
	  echo "ship: the tree is dirty — the image tag would name a commit that is not what is built"; \
	  git status --short; exit 1; \
	fi

# Both images are pushed by tag, and pushing one that is already there is cheap — the layers
# exist, so it is a manifest check. That is why every tier target pushes rather than trusting
# a previous one to have: `make ship-versed` alone must be a whole ship.
.PHONY: ship-push ship-push-coderunner
ship-push: ship-clean
	@echo "ship: staging → $(STAGING_SHA)"
	$(MAKE) push IMAGE_ARCH=amd64 PUSH_IMAGE=$(SHIP_IMAGE)
ship-push-coderunner: ship-clean
	@echo "ship: staging coderunner → $(STAGING_SHA)"
	$(MAKE) coderunner-push IMAGE_ARCH=amd64 CODERUNNER_IMAGE=$(SHIP_CODERUNNER)

# The apply half, shared: staging's profile, the sha-tagged images, one tier, and block on it.
SHIP_DEPLOY = $(MAKE) deploy PROFILE=staging WAIT=1 IMAGE=$(SHIP_IMAGE) \
	  E="-e coderunnerImage=$(SHIP_CODERUNNER)"

.PHONY: ship-docd ship-versed ship-ios ship-coderunner
ship-docd: ship-push ## ship the store tier alone, and wait for it (roll this FIRST)
	$(SHIP_DEPLOY) SELECT=tier=docd
ship-versed: ship-push ## ship the engine alone, and wait for it (after docd)
	$(SHIP_DEPLOY) SELECT=tier=versed
ship-ios: ship-push ## ship the io connectors alone, and wait for them
	$(SHIP_DEPLOY) SELECT=tier=io
ship-coderunner: ship-push-coderunner ## ship the coderunner alone (its own image), and wait for it
	$(SHIP_DEPLOY) SELECT=tier=runner

# make deploy PROFILE=minikube                     # default SC (no mount)
# make deploy PROFILE=minikube HOSTPV=true HOSTPATH=/mnt/verse   # host-linked (needs --mount)
# make deploy PROFILE=staging                      # the profile's own registry ref
# make deploy PROFILE=staging IMAGE=example/verse-staging:v3    # …a particular build of it
# make deploy E='-e gitIO=true -e slackIO=true'          # …plus source connectors
#
# `-O yaml` is not decoration. The default output is TONY, which for block-style input happens
# to also be valid YAML — every manifest here was, so this went unnoticed until one used flow
# mappings. Tony spells a multi-entry map `{` newline-separated pairs `}`, and YAML wants
# commas in flow style, so kubectl refuses the stream with a parse error pointing at a line
# number in a document nobody wrote by hand. Ask for the format we are piping into.
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

# WHERE this goes, which `kubectl apply` otherwise takes from whatever context your shell is
# pointing at. That makes this target a function of ambient state: the command that deploys
# staging to staging is character for character the command that deploys it to production, and
# the difference is something you did in another terminal an hour ago. So a profile NAMES its
# cluster (`kubeContext`) and we pass it explicitly. Empty falls back to the current context —
# right for minikube, and for any cluster deploy/ has never heard of.
#
# It prints where it is going before it goes, because a guard you cannot see is a guard you
# stop believing in. CONTEXT= overrides, for a cluster that is the same shape under a different
# name (a second staging, a colleague's kubeconfig entry).
CONTEXT ?=
# SELECT applies ONE TIER out of the render — `SELECT=tier=docd` — and WAIT= makes the target
# block until what it applied is rolled out. Both are here rather than in a target of their own
# because the alternative is a second copy of the context resolution above, and two ways to
# say where a deploy goes is exactly the ambient-state problem that comment is about. The
# ship-<tier> targets below are what use them; see there for why a tier ships alone.
SELECT ?=
WAIT   ?=
WAIT_TIMEOUT ?= 5m
.PHONY: deploy
deploy: ## render deploy/ and kubectl apply (PROFILE=minikube|staging; CONTEXT= to override the target cluster; IMAGE= to pin one; HOSTPV/HOSTPATH to host-link; SELECT=tier=x for one tier; WAIT=1 to block on the rollout; E='-e k=v …')
	@ctx='$(CONTEXT)'; \
	if [ -z "$$ctx" ]; then \
	  ctx=$$($(O) build deploy $(if $(PROFILE),-p $(PROFILE),) -s | sed -n 's/^kubeContext: *//p' | tr -d '"'); \
	fi; \
	echo "deploy: $(if $(PROFILE),profile '$(PROFILE)',defaults)$(if $(SELECT), [$(SELECT)],) → context '$${ctx:-$$(kubectl config current-context)}'"; \
	$(O) build deploy -O yaml $(if $(PROFILE),-p $(PROFILE),) $(if $(DEPLOY_IMAGE),-e image=$(DEPLOY_IMAGE),) $(if $(HOSTPV),-e hostPV=$(HOSTPV),) $(if $(HOSTPATH),-e hostPath=$(HOSTPATH),) $(E) \
	  | kubectl $${ctx:+--context "$$ctx"} apply $(if $(SELECT),-l $(SELECT),) -f - || exit 1; \
	if [ -n "$(WAIT)" ]; then \
	  ns=$$($(O) build deploy $(if $(PROFILE),-p $(PROFILE),) -s | sed -n 's/^namespace: *//p' | tr -d '"'); \
	  for obj in $$(kubectl $${ctx:+--context "$$ctx"} -n "$$ns" get deploy,statefulset $(if $(SELECT),-l $(SELECT),) -o name); do \
	    kubectl $${ctx:+--context "$$ctx"} -n "$$ns" rollout status "$$obj" --timeout=$(WAIT_TIMEOUT) || exit 1; \
	  done; \
	fi

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

.PHONY: check-paths
check-paths: ## fail if the system:kind:id vocabulary moves off its baseline (it only goes down)
	@./scripts/check-paths.sh

# A diagram is the one thing in this tree nothing reads until a browser does, months later —
# mkdocs copies the block through untouched, so a syntax error is invisible at build time and
# surfaces as mermaid's error text printed where the picture should be. This parses every block
# with the mermaid the SITE loads, asking mkdocs-mermaid2-plugin which version that is.
#
# It needs node, which this repo otherwise does not, so it SKIPS loudly rather than failing
# when there is none: `make check` has to stay runnable on a machine with nothing but Go.
# The claims are in one file and the work is in three places, so the join is derived rather than
# kept: a note carries a `Claimed:` line only if it wants to, and this fails when one says
# something owners.tony does not. A note that says nothing is free to be free — the only way to
# be wrong is to say something, which is the property worth checking.
.PHONY: check-claims
check-claims: ## fail if a note's Claimed: line disagrees with docs/working/owners.tony
	@./scripts/claims.sh --check

.PHONY: claims
claims: ## who holds which work, what has lapsed, and what is free
	@./scripts/claims.sh

.PHONY: check-mermaid
check-mermaid: ## fail if a ```mermaid block does not parse (skips if node is absent)
	@./scripts/check-mermaid.sh

# The docs BUILD, which is a different question from whether their links resolve on disk: mkdocs
# serves docs/ and nothing above it, so a link out of the tree (`../../scripts/x.sh`) resolves
# for a reader of the repo and 404s for a reader of the site. --strict makes that a failure.
#
# A link into articles/, roadmap/ or working/ 404s the same way, because mkdocs.yml excludes
# them, but mkdocs reports a link to an excluded page at INFO whatever --strict and the
# validation levels say. So its message is matched here, and that line is the failure.
#
# It skips loudly when mkdocs is absent, for the reason check-mermaid skips without node: `make
# check` stays runnable on a machine with nothing but Go. `pip install -r requirements.txt`.
.PHONY: check-docs
check-docs: ## fail if the docs do not build (skips if mkdocs is absent)
	@if command -v mkdocs >/dev/null 2>&1; then \
		out="$$(mkdocs build --strict --site-dir "$$(mktemp -d)/site" 2>&1)" || { echo "$$out"; exit 1; }; \
		if echo "$$out" | grep 'excluded from the built site'; then exit 1; fi; \
		echo "the docs build"; \
	else \
		echo "mkdocs absent — skipping the docs build (pip install -r requirements.txt)"; \
	fi

.PHONY: check
check: check-generate check-json check-paths check-claims check-mermaid check-docs ## every static check (what CI should run before the tests)

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
