# Image URL to use all building/pushing image targets
IMG ?= inference-operator:latest

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

# Setting SHELL to bash allows bash commands to be executed by recipes.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.PHONY: all
all: build

##@ General

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: lint
lint: ## Run golangci-lint against code.
	golangci-lint run

.PHONY: test
test: fmt vet ## Run tests.
	go test ./... -coverprofile cover.out

.PHONY: test-e2e
test-e2e: ## Run e2e tests.
	go test ./test/e2e/... -v -timeout 30m

.PHONY: test-integration
test-integration: ## Run integration tests.
	go test ./test/integration/... -v -timeout 15m

##@ Build

.PHONY: build
build: fmt vet ## Build manager binary.
	go build -o bin/manager cmd/operator/main.go

.PHONY: run
run: fmt vet ## Run a controller from your host.
	go run ./cmd/operator/main.go

.PHONY: docker-build
docker-build: ## Build docker image with the manager.
	docker build -t ${IMG} .

.PHONY: docker-push
docker-push: ## Push docker image with the manager.
	docker push ${IMG}

.PHONY: docker-buildx
docker-buildx: ## Build and push docker image for cross-platform support.
	docker buildx build --platform linux/amd64,linux/arm64 --push -t ${IMG} .

##@ Deployment

ifndef ignore-not-found
  ignore-not-found = false
endif

.PHONY: install
install: ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	kubectl apply -f config/crd/bases/

.PHONY: uninstall
uninstall: ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config.
	kubectl delete --ignore-not-found=$(ignore-not-found) -f config/crd/bases/

.PHONY: deploy
deploy: ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	kubectl apply -f config/rbac/
	kubectl apply -f config/manager/

.PHONY: undeploy
undeploy: ## Undeploy controller from the K8s cluster specified in ~/.kube/config.
	kubectl delete --ignore-not-found=$(ignore-not-found) -f config/manager/
	kubectl delete --ignore-not-found=$(ignore-not-found) -f config/rbac/

.PHONY: deploy-samples
deploy-samples: ## Deploy sample CRs.
	kubectl apply -f config/samples/

.PHONY: undeploy-samples
undeploy-samples: ## Remove sample CRs.
	kubectl delete --ignore-not-found=$(ignore-not-found) -f config/samples/

##@ Kind Cluster

.PHONY: kind-create
kind-create: ## Create a kind cluster for development.
	kind create cluster --name inference-operator --config hack/kind-config.yaml

.PHONY: kind-delete
kind-delete: ## Delete the kind cluster.
	kind delete cluster --name inference-operator

.PHONY: kind-load
kind-load: docker-build ## Load docker image into kind cluster.
	kind load docker-image ${IMG} --name inference-operator

.PHONY: deploy-kind
deploy-kind: kind-load install deploy ## Deploy to kind cluster.
	@echo "Deployed to kind cluster"

##@ Generate

.PHONY: generate
generate: ## Generate code (deepcopy, etc.)
	controller-gen object:headerFile="hack/boilerplate.go.txt" paths="./..."

.PHONY: manifests
manifests: ## Generate CRD manifests.
	controller-gen crd paths="./..." output:crd:artifacts:config=config/crd/bases

##@ Tools

CONTROLLER_GEN = $(shell pwd)/bin/controller-gen
.PHONY: controller-gen
controller-gen: ## Download controller-gen locally if necessary.
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen@v0.14.0)

GOLANGCI_LINT = $(shell pwd)/bin/golangci-lint
.PHONY: golangci-lint
golangci-lint: ## Download golangci-lint locally if necessary.
	$(call go-install-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/cmd/golangci-lint@v1.55.2)

# go-install-tool will 'go install' any package with custom target and target name
define go-install-tool
@[ -f $(1) ] || { \
set -e; \
echo "Downloading $(2)"; \
GOBIN=$(shell pwd)/bin go install $(2); \
}
endef
