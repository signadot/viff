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
NAME      ?= viff
TAG       ?= dev
IMAGE     ?= $(NAME):$(TAG)                 # local dev tag (docker-desktop/minikube)
PUSH_IMAGE?= $(REGISTRY)/$(NAME):$(TAG)     # registry ref for `make push`
IMAGE_ARCH?= $(GOARCH)

RUNNER_IMAGE     ?= $(REGISTRY)/viff-runner:$(TAG)         # the viff runner, its own image (deploy/viff-runner)
CODEX_RUNNER_IMAGE ?= $(REGISTRY)/viff-runner:codex-$(TAG)  # the viff runner with Codex in it (deploy/codex-runner)
CLAUDE_RUNNER_IMAGE ?= $(REGISTRY)/viff-runner:claude-$(TAG)  # the viff runner with Claude Code in it (deploy/claude-runner)

DIST := dist

O_PKG := github.com/signadot/tony-format/go-tony/cmd/o

# Four binaries, one image:
#   viff    the engine + CLI
#   o        the tony daemons viff spawns for logd+docd (pinned, built alongside viff)
#   iod  the shipped Sources (git, slack, linear) — a SEPARATE process by design, so the
#            engine binary carries no connector's wire format or credential handling. Riding
#            in the same image is free; riding in the same process is not (cmd/iod).
#   runnerd  the shipped runners (viff) — separate for the same reason (cmd/runnerd).
#   viff-call-tool
#            a task's tools from the shell (cmd/viff-call-tool): the runner finds it beside
#            itself and copies it into each task's workspace, for a program running there.
#   viff-secret-exec
#            one command run with a secret from the shell, as if in place (cmd/viff-secret-exec):
#            beside viff-call-tool, where a task's script finds both.
define go_build
	CGO_ENABLED=0 GOOS=$(1) GOARCH=$(2) $(GO) build -o $(DIST)/$(1)/$(2)/viff ./cmd/viff
	CGO_ENABLED=0 GOOS=$(1) GOARCH=$(2) $(GO) build -o $(DIST)/$(1)/$(2)/o $(O_PKG)
	CGO_ENABLED=0 GOOS=$(1) GOARCH=$(2) $(GO) build -o $(DIST)/$(1)/$(2)/iod ./cmd/iod
	CGO_ENABLED=0 GOOS=$(1) GOARCH=$(2) $(GO) build -o $(DIST)/$(1)/$(2)/runnerd ./cmd/runnerd
	CGO_ENABLED=0 GOOS=$(1) GOARCH=$(2) $(GO) build -o $(DIST)/$(1)/$(2)/viff-call-tool ./cmd/viff-call-tool
	CGO_ENABLED=0 GOOS=$(1) GOARCH=$(2) $(GO) build -o $(DIST)/$(1)/$(2)/viff-secret-exec ./cmd/viff-secret-exec
endef

.DEFAULT_GOAL := help

.PHONY: bin
bin: ## build viff + o + iod + runnerd + viff-call-tool + viff-secret-exec for the host → dist/$(GOOS)/$(GOARCH)/ (dev use)
	$(call go_build,$(GOOS),$(GOARCH))

.PHONY: iod
iod: ## build just the source connectors (git, slack, linear) for the host
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) $(GO) build -o $(DIST)/$(GOOS)/$(GOARCH)/iod ./cmd/iod

.PHONY: runnerd
runnerd: ## build just the runners (viff) for the host
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) $(GO) build -o $(DIST)/$(GOOS)/$(GOARCH)/runnerd ./cmd/runnerd

.PHONY: viff-call-tool
viff-call-tool: ## build just the shell's tool caller for the host
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) $(GO) build -o $(DIST)/$(GOOS)/$(GOARCH)/viff-call-tool ./cmd/viff-call-tool

.PHONY: viff-secret-exec
viff-secret-exec: ## build just the shell's command-with-a-secret for the host
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) $(GO) build -o $(DIST)/$(GOOS)/$(GOARCH)/viff-secret-exec ./cmd/viff-secret-exec

.PHONY: dist-linux
dist-linux: ## cross-compile viff + o + iod + runnerd + viff-call-tool + viff-secret-exec for linux/$(IMAGE_ARCH) → dist/linux/$(IMAGE_ARCH)/ (for the image)
	$(call go_build,linux,$(IMAGE_ARCH))

.PHONY: docker
docker: dist-linux ## build the image, COPYing the pre-built linux binaries (no go build in docker)
	$(DOCKER) build --platform linux/$(IMAGE_ARCH) --build-arg TARGETARCH=$(IMAGE_ARCH) -t $(IMAGE) -f build/Dockerfile .

# The viff runner is its OWN image and its own target. It is not folded into `docker` because the
# two images have nothing to do with each other's cadence: the runtime image is thin and pinned
# to the engine, this one is sized by the toolchain the repositories it serves need to build.
# viff-call-tool and viff-secret-exec ride beside runnerd, where the runner finds them for a
# task's scripts.
.PHONY: runner-linux
runner-linux: ## cross-compile runnerd, viff-call-tool and viff-secret-exec for linux/$(IMAGE_ARCH)
	CGO_ENABLED=0 GOOS=linux GOARCH=$(IMAGE_ARCH) $(GO) build \
	  -o $(DIST)/linux/$(IMAGE_ARCH)/runnerd ./cmd/runnerd
	CGO_ENABLED=0 GOOS=linux GOARCH=$(IMAGE_ARCH) $(GO) build \
	  -o $(DIST)/linux/$(IMAGE_ARCH)/viff-call-tool ./cmd/viff-call-tool
	CGO_ENABLED=0 GOOS=linux GOARCH=$(IMAGE_ARCH) $(GO) build \
	  -o $(DIST)/linux/$(IMAGE_ARCH)/viff-secret-exec ./cmd/viff-secret-exec

.PHONY: runner-docker
runner-docker: runner-linux ## build the viff runner image (bubblewrap containment)
	$(DOCKER) build --platform linux/$(IMAGE_ARCH) --build-arg TARGETARCH=$(IMAGE_ARCH) \
	  -t $(RUNNER_IMAGE) -f deploy/viff-runner/Dockerfile .

# No RUNNER_PUSH_IMAGE to pair with PUSH_IMAGE: RUNNER_IMAGE already carries the registry, so
# there is nothing to retag. Override it whole — including into the same repository as the
# runtime image under a distinguishing tag, which is one repository to create and one pull
# credential to grant:
#
#   make runner-push IMAGE_ARCH=amd64 RUNNER_IMAGE=<registry>/viff-runner:<tag>
.PHONY: runner-push
runner-push: runner-docker ## build + push $(RUNNER_IMAGE)
	$(DOCKER) push $(RUNNER_IMAGE)

# The codex runner: the viff runner image plus a pinned Codex, for the codex agent. A tag in
# the runner's repository rather than a repository of its own, for the reason the comment above
# gives.
.PHONY: codex-runner-docker
codex-runner-docker: runner-linux ## build the codex runner image (viff runner + Codex)
	$(DOCKER) build --platform linux/$(IMAGE_ARCH) --build-arg TARGETARCH=$(IMAGE_ARCH) \
	  -t $(CODEX_RUNNER_IMAGE) -f deploy/codex-runner/Dockerfile .

.PHONY: codex-runner-push
codex-runner-push: codex-runner-docker ## build + push $(CODEX_RUNNER_IMAGE)
	$(DOCKER) push $(CODEX_RUNNER_IMAGE)

# The claude runner: the viff runner image plus a pinned Claude Code, for the claude agent, a
# tag in the runner's repository for the same reason.
.PHONY: claude-runner-docker
claude-runner-docker: runner-linux ## build the claude runner image (viff runner + Claude Code)
	$(DOCKER) build --platform linux/$(IMAGE_ARCH) --build-arg TARGETARCH=$(IMAGE_ARCH) \
	  -t $(CLAUDE_RUNNER_IMAGE) -f deploy/claude-runner/Dockerfile .

.PHONY: claude-runner-push
claude-runner-push: claude-runner-docker ## build + push $(CLAUDE_RUNNER_IMAGE)
	$(DOCKER) push $(CLAUDE_RUNNER_IMAGE)

.PHONY: load
load: docker ## build + load the image into minikube (Docker Desktop k8s needs no load)
	minikube image load $(IMAGE)

.PHONY: push
push: docker ## build + push $(PUSH_IMAGE) to the registry
	$(DOCKER) tag $(IMAGE) $(PUSH_IMAGE)
	$(DOCKER) push $(PUSH_IMAGE)

# make deploy PROFILE=minikube                     # default SC (no mount)
# make deploy PROFILE=minikube HOSTPV=true HOSTPATH=/mnt/viff   # host-linked (needs --mount)
# make deploy PROFILE=eks                          # EKS: gp3, the EKS issuer (deploy/profiles/eks.tony)
# make deploy PROFILE=eks IMAGE=<registry>/viff:<sha>   # …a particular build of it
# make deploy E='-e gitIod=true -e slackIod=true'          # …plus source connectors
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
# deployed the local dev tag to a cluster where no node can pull it. So:
#
#   no PROFILE  — nothing else names one; pass $(IMAGE), the local flow's just-built tag.
#   a PROFILE   — it names its own (minikube: viff:dev, eks: its own ref),
#                 so stay quiet unless this invocation named one itself.
#
# `origin` is what tells "you said IMAGE/NAME/TAG" from the `?=` defaults at the top of this
# file: those report `file`, a command-line or exported one reports otherwise. NAME/TAG count
# as naming one, but they compose into $(NAME):$(TAG) — the SHARED repository — so on a cluster
# `TAG=v9` renders `viff:v9` and fails to pull. Name IMAGE= outright there.
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
# a deployment ships one tier at a time with them (deploy/README.md, "Shipping one tier at a time").
SELECT ?=
WAIT   ?=
WAIT_TIMEOUT ?= 5m
.PHONY: deploy
deploy: ## render deploy/ and kubectl apply (PROFILE=minikube|eks|<path to a profile>; CONTEXT= to override the target cluster; IMAGE= to pin one; HOSTPV/HOSTPATH to host-link; SELECT=tier=x for one tier; WAIT=1 to block on the rollout; E='-e k=v …')
	@show=$$($(O) build deploy $(if $(PROFILE),-p $(PROFILE),) $(E) -s) || { echo "deploy: the environment does not evaluate; nothing applied"; exit 1; }; \
	ctx='$(CONTEXT)'; \
	if [ -z "$$ctx" ]; then \
	  ctx=$$(printf '%s\n' "$$show" | sed -n 's/^kubeContext: *//p' | tr -d '"'); \
	fi; \
	case "$$ctx" in '<'*'>') echo "deploy: the profile's kubeContext is a placeholder, $$ctx: fill it in, or say CONTEXT="; exit 1;; esac; \
	if printf '%s\n' "$$show" | awk '/^docdPool:/{p=$$2} /^docdPoolLabel:/{l=$$2} END{exit !(p!="" && p!="\"\"" && (l=="" || l=="\"\""))}'; then \
	  echo "deploy: docdPool is set and docdPoolLabel is empty, so the selector would have no key: name the pool's label"; exit 1; \
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
# build path is what lets build/Dockerfile just COPY binaries.
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

# viff renders through package codec; a stray encoding/json is how that quietly stops
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

# The docs BUILD, which is a different question from whether their links resolve on disk: mkdocs
# serves docs/ and nothing above it, so a link out of the tree (`../../scripts/x.sh`) resolves
# for a reader of the repo and 404s for a reader of the site. scripts/mkdocs_repo_links.py
# rewrites one to the file on GitHub when the file exists, and --strict makes any other a failure.
#
# A link into articles/ 404s the same way, because mkdocs.yml excludes
# them, but mkdocs reports a link to an excluded page at INFO whatever --strict and the
# validation levels say. So its message is matched here, and that line is the failure.
#
# It skips loudly when mkdocs is absent, for the reason check-mermaid skips without node: `make
# check` stays runnable on a machine with nothing but Go. `pip install -r docs/requirements.txt`.
.PHONY: check-docs
check-docs: ## fail if the docs do not build (skips if mkdocs is absent)
	@if command -v mkdocs >/dev/null 2>&1; then \
		out="$$(mkdocs build --strict --site-dir "$$(mktemp -d)/site" 2>&1)" || { echo "$$out"; exit 1; }; \
		if echo "$$out" | grep 'excluded from the built site'; then exit 1; fi; \
		echo "the docs build"; \
	else \
		echo "mkdocs absent — skipping the docs build (pip install -r docs/requirements.txt)"; \
	fi

# pkg/ is what a program outside the viff imports; internal/ is viffd's own. The compiler does
# not stop one from importing the other inside the module, so this does: the dependency closure
# of pkg/ must hold nothing from internal/. Anchored on the module path, because stdlib and
# third-party packages have internal/ directories of their own. -test holds the tests to it as
# well: a test of a package under pkg/ that needs a running viffd lives under internal/iotests,
# beside what it needs, rather than importing internal/ from pkg/.
.PHONY: check-layers
check-layers: ## fail if a package under pkg/, or one of its tests, depends on one under internal/
	@bad="$$($(GO) list -deps -test ./pkg/... | grep 'go.viff.dev/viff/internal/' || true)"; \
	  [ -z "$$bad" ] || { echo "pkg/ imports internal/:"; echo "$$bad" | sed 's/^/  /'; exit 1; }
	@echo "pkg/ imports nothing from internal/"

.PHONY: check
check: check-generate check-json check-layers check-mermaid check-docs ## every static check (what CI should run before the tests)

.PHONY: smoke
smoke: ## end-to-end smoke of the core surfaces (isolated in-memory server on an ephemeral port)
	@./test/smoke.sh

# Note the difference from smoke: smoke builds its own binaries and its own server and
# reaps both; this one writes into YOUR running viff and leaves it there to play with.
.PHONY: seed-demo
seed-demo: ## seed a RUNNING viff with a demo charter + entities (./scripts/seed-demo.sh --help)
	@./scripts/seed-demo.sh

# The two the tutorial drives, split so each section seeds only what it is about: a world of
# entities with nothing reacting to them, and the rules that react, one shape at a time.
.PHONY: seed-entities
seed-entities: ## seed a RUNNING viff with a world of entities and no charter (the tutorial's data page)
	@./scripts/seed-entities.sh

.PHONY: shapes
shapes: ## list the charter shapes (./scripts/charter-shapes.sh run <shape> to install and fire one)
	@./scripts/charter-shapes.sh

# Correlation across systems, run rather than argued: three id schemes, the derived link the
# engine names f(rule, subject), and the set read that reassembles the chain.
.PHONY: lineage
lineage: ## seed a RUNNING viff with a commit → PR → release chain (./scripts/lineage.sh --help)
	@./scripts/lineage.sh

# The representation is a DEFAULT, not a fixture: tony out of the box, JSON when asked for.
# Running the suite only one way would let the other rot silently — and the JSON path is what
# every existing client and `curl | jq` still uses. This is the target CI should call.
.PHONY: smoke-all
smoke-all: ## smoke under both renderings (tony default, then VIFF_OUTPUT_FORMAT=yaml)
	@./test/smoke.sh
	@VIFF_OUTPUT_FORMAT=yaml ./test/smoke.sh

.PHONY: clean
clean: ## remove dist/
	rm -rf $(DIST)

.PHONY: help
help: ## list targets
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | sort | \
	  awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'
