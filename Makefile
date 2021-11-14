REPO=blacktop
NAME=go-apfs
CLI=github.com/blacktop/go-apfs/cmd/apfs
CUR_VERSION=$(shell svu current)
NEXT_VERSION=$(shell svu patch)

APFS_DMG:=


.PHONY: build-deps
build-deps: ## Install the build dependencies
	@echo " > Installing build deps"
	brew install go goreleaser caarlos0/tap/svu
	go install golang.org/x/tools/cmd/stringer

.PHONY: build
build: ## Build apfs locally
	@echo " > Building locally"
	CGO_ENABLED=1 go build -o apfs.${CUR_VERSION} ./cmd/apfs

.PHONY: test
test: build ## Test apfs (list root dir on APFS_DMG)
	ifndef APFS_DMG
		$(error APFS_DMG variable is not set)
	endif
	@echo " > Listing ROOT dir"
	@$(PWD)/apfs.${CUR_VERSION} ls ${APFS_DMG}

.PHONY: dry_release
dry_release: ## Run goreleaser without releasing/pushing artifacts to github
	@echo " > Creating Pre-release Build ${NEXT_VERSION}"
	@goreleaser build --rm-dist --skip-validate --id darwin

.PHONY: snapshot
snapshot: ## Run goreleaser snapshot
	@echo " > Creating Snapshot ${NEXT_VERSION}"
	@goreleaser --rm-dist --snapshot

.PHONY: release
release: ## Create a new release from the NEXT_VERSION
	@echo " > Creating Release ${NEXT_VERSION}"
	@.hack/make/release ${NEXT_VERSION}
	@goreleaser --rm-dist

.PHONY: destroy
destroy: ## Remove release for the CUR_VERSION
	@echo " > Deleting Release"
	git tag -d ${CUR_VERSION}
	git push origin :refs/tags/${CUR_VERSION}

clean: ## Clean up artifacts
	@echo " > Cleaning"
	rm -rf dist
	rm apfs.v* || true

# Absolutely awesome: http://marmelab.com/blog/2016/02/29/auto-documented-makefile.html
help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'

.DEFAULT_GOAL := help