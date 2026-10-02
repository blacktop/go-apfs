CUR_VERSION:=$(shell svu current)
NEXT_VERSION:=$(shell svu patch)

APFS_DMG:=

SHELL := /bin/bash

.PHONY: build-deps
build-deps: ## Install the build and release dependencies
	@echo " > Installing build deps"
	brew install go goreleaser caarlos0/tap/svu zig gnupg
	go install golang.org/x/tools/cmd/stringer@latest

.PHONY: build
build: ## Build apfs locally
	@echo " > Building locally"
	@cd cmd/apfs; go build -o ../../apfs.${CUR_VERSION} .

.PHONY: test
test: ## Run the Go test suite
	@echo " > Running tests"
	@go test ./...

.PHONY: smoke
smoke: build ## List the root dir of APFS_DMG with a local build
	@test -n "${APFS_DMG}" || { echo "set APFS_DMG=path/to/apfs.dmg" >&2; exit 1; }
	@echo " > Listing ROOT dir"
	@./apfs.${CUR_VERSION} ls "${APFS_DMG}"

.PHONY: test-dmgs
test-dmgs: ## Regenerate the committed test DMG fixtures
	@echo " > Creating test DMGs"
	@hdiutil create -volname TEST -srcfolder README.md -ov -format UDZO testdata/test.dmg
	@echo -n "password" | hdiutil create -volname SECURE -srcfolder README.md -ov -format UDZO -encryption -stdinpass -fs apfs testdata/secure.dmg

.PHONY: dry_release
dry_release: ## Run goreleaser without releasing/pushing artifacts to github
	@echo " > Creating Pre-release Build ${NEXT_VERSION}"
	@GOROOT=$(shell go env GOROOT) goreleaser build --id darwin --clean --timeout 60m --snapshot --single-target --output dist/apfs

.PHONY: snapshot
snapshot: ## Run goreleaser snapshot
	@echo " > Creating Snapshot ${NEXT_VERSION}"
	@GOROOT=$(shell go env GOROOT) goreleaser --clean --timeout 60m --snapshot

.PHONY: check-release
check-release: ## Check the tree is clean, on main, in sync with origin, and passing tests
	@test -z "$$(git status --porcelain)" || { echo "working tree is dirty: commit or stash first" >&2; exit 1; }
	@test "$$(git branch --show-current)" = main || { echo "releases are cut from main" >&2; exit 1; }
	@git fetch --quiet origin main
	@test "$$(git rev-parse HEAD)" = "$$(git rev-parse origin/main)" || { echo "HEAD differs from origin/main: pull or push first" >&2; exit 1; }
	@go test ./...

.PHONY: release
release: check-release ## Tag NEXT_VERSION, point the CLI at it, and publish (override with NEXT_VERSION=vX.Y.Z)
	@echo " > Creating Release ${NEXT_VERSION}"
	@hack/make/release ${NEXT_VERSION}
	@GOROOT=$(shell go env GOROOT) goreleaser --clean --timeout 60m --skip=validate

.PHONY: destroy
destroy: ## Delete the CUR_VERSION tag (the Go proxy keeps that version, so never reuse it)
	@echo " > Deleting Release"
	git tag -d ${CUR_VERSION}
	git push origin :refs/tags/${CUR_VERSION}

.PHONY: clean
clean: ## Clean up build artifacts
	@echo " > Cleaning"
	rm -rf dist completions
	rm -f apfs.v*

# Absolutely awesome: http://marmelab.com/blog/2016/02/29/auto-documented-makefile.html
.PHONY: help
help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'

.DEFAULT_GOAL := help
