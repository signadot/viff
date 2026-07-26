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

# NOTE: no package carries a //go:generate tony-codegen directive, so these are no-ops
# today — but nothing blocks it any more. Every defect that did is fixed as of go-tony
# v0.0.89 (tony-format#f69agjyeh12ksa25cnn0 items 1-4, 6-8, 10, 11).
#
# What changed with v0.0.89 is the CASE for codegen, not the ability. gomap's reflection
# path now honors omitzero/omit itself, so verse's ~130-line pruning pass is gone and
# reflection alone produces correct documents. Codegen is therefore no longer needed for
# correctness; what it still offers is schema_gen.tony artifacts (validation, docs, docd
# registration) and no reflection at run time, against the cost of committed generated
# files and a staleness check. That is a live choice rather than a blocked one.
#
# The struct tags are already exactly the ones tony-codegen reads, so adopting it changes
# no type declaration — only the charter grammar in package trigger stays hand-written, via
# codec=custom.
#
# Codegen is deliberately NOT a dependency of `bin`/`dist-linux`: keeping it out of the
# build path is what lets the Dockerfile just COPY binaries.
.PHONY: generate
generate: ## regenerate tony codegen (currently a no-op — see the note above)
	$(GO) generate ./...

.PHONY: check-generate
check-generate: ## fail if the generated files are stale (CI)
	$(GO) generate ./...
	git diff --exit-code

.PHONY: test
test: ## go test ./...
	$(GO) test ./...

.PHONY: smoke
smoke: ## end-to-end smoke of the core surfaces (isolated in-memory server on a free port)
	@./smoke.sh

.PHONY: clean
clean: ## remove dist/
	rm -rf $(DIST)

.PHONY: help
help: ## list targets
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | sort | \
	  awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'
