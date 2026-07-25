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
