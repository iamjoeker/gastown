.PHONY: build desktop-build desktop-run install safe-install sync-plugins check-forward-only check-version-tag check-install-path clean test test-makefile test-e2e-container check-up-to-date

BINARY := gt
BINARY_DESKTOP := gt-desktop
BUILD_DIR := .
INSTALL_DIR := $(HOME)/.local/bin
E2E_IMAGE ?= gastown-test
E2E_BUILD_FLAGS ?=
E2E_RUN_FLAGS ?= --rm
E2E_BUILD_RETRIES ?= 1
E2E_RUN_RETRIES ?= 1

# Get version info for ldflags.
#
# Every git call is pinned with -C to the directory holding THIS makefile, so
# the stamp always describes the gastown source tree being compiled — never
# whatever repository the caller happened to be standing in (`make -C`,
# `make -f`, a build driven from the town repo). Deriving provenance from an
# ambient repo is exactly the defect gt-5mvj filed against `gt version`.
SRC_DIR := $(patsubst %/,%,$(dir $(abspath $(lastword $(MAKEFILE_LIST)))))
SRC_GIT := git -C $(SRC_DIR)

VERSION := $(shell $(SRC_GIT) describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT := $(shell $(SRC_GIT) rev-parse HEAD 2>/dev/null || echo "")
BRANCH := $(shell $(SRC_GIT) symbolic-ref --short HEAD 2>/dev/null || echo "")
BUILD_TIME := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

LDFLAGS := -s -w \
           -X github.com/steveyegge/gastown/internal/cmd.Version=$(VERSION) \
           -X github.com/steveyegge/gastown/internal/cmd.Commit=$(COMMIT) \
           -X github.com/steveyegge/gastown/internal/cmd.Branch=$(BRANCH) \
           -X github.com/steveyegge/gastown/internal/cmd.BuildTime=$(BUILD_TIME) \
           -X github.com/steveyegge/gastown/internal/cmd.BuiltProperly=1

# ICU4C detection for macOS (required by go-icu-regex transitive dependency).
# Homebrew installs icu4c as a keg-only package, so headers/libs aren't on the
# default search path. Auto-detect the prefix and export CGo flags.
ifeq ($(shell uname),Darwin)
  ICU_PREFIX := $(shell brew --prefix icu4c 2>/dev/null)
  ifneq ($(ICU_PREFIX),)
    export CGO_CPPFLAGS += -I$(ICU_PREFIX)/include
    export CGO_LDFLAGS  += -L$(ICU_PREFIX)/lib
  endif
endif

build:
	go build -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY)-proxy-server ./cmd/gt-proxy-server
	go build -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY)-proxy-client ./cmd/gt-proxy-client
	go build -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY) ./cmd/gt

desktop-build:
	go build -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_DESKTOP) ./cmd/gt-desktop

desktop-run:
	go run ./cmd/gt-desktop

check-up-to-date:
ifndef SKIP_UPDATE_CHECK
	@# Skip check on detached HEAD (tag checkouts, CI builds)
	@if ! git symbolic-ref HEAD >/dev/null 2>&1; then exit 0; fi
	@# Use the current branch's tracking ref (works for main, carry/operational, etc.)
	@UPSTREAM=$$(git rev-parse --abbrev-ref --symbolic-full-name @{u} 2>/dev/null); \
	if [ -z "$$UPSTREAM" ]; then \
		echo "Warning: no upstream tracking branch set, skipping update check"; \
		exit 0; \
	fi; \
	REMOTE_NAME=$$(echo "$$UPSTREAM" | cut -d/ -f1); \
	REMOTE_BRANCH=$$(echo "$$UPSTREAM" | cut -d/ -f2-); \
	git fetch "$$REMOTE_NAME" "$$REMOTE_BRANCH" --quiet 2>/dev/null || true; \
	LOCAL=$$(git rev-parse HEAD 2>/dev/null); \
	REMOTE=$$(git rev-parse "$$UPSTREAM" 2>/dev/null); \
	if [ -n "$$REMOTE" ] && [ "$$LOCAL" != "$$REMOTE" ]; then \
		echo "ERROR: Local branch is not up to date with $$UPSTREAM"; \
		echo "  Local:  $$(git rev-parse --short HEAD)"; \
		echo "  Remote: $$(git rev-parse --short $$UPSTREAM)"; \
		echo "Run 'git pull' first, or use SKIP_UPDATE_CHECK=1 to override"; \
		exit 1; \
	fi
endif

# check-forward-only: Ensure HEAD is a descendant of the currently installed binary's commit.
# Prevents rebuilding to an older or diverged commit, which caused a crash loop where
# the replaced binary broke session startup hooks → witness respawned → loop every 1-2 min.
#
# The binary's commit comes from `gt version --commit`, which reports only a
# link-time stamp and prints "unknown" otherwise. The older `@sha` scrape is
# kept as a fallback because this target runs against the *installed* binary,
# which may predate --commit; it is only accepted when it looks like a sha, so
# an "unknown flag" usage message cannot be mistaken for one. Note the scrape
# was itself unreliable: with no branch stamped there is no `@` in the line, so
# it silently yielded nothing and the downgrade guard skipped. (gt-5mvj)
check-forward-only:
ifndef SKIP_FORWARD_CHECK
	@BINARY_COMMIT=$$($(INSTALL_DIR)/$(BINARY) version --commit 2>/dev/null | head -1); \
	case "$$BINARY_COMMIT" in \
		unknown|[0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]*) ;; \
		*) BINARY_COMMIT=$$($(INSTALL_DIR)/$(BINARY) version --verbose 2>/dev/null | grep -o '@[a-f0-9]*' | head -1 | tr -d '@') ;; \
	esac; \
	if [ -n "$$BINARY_COMMIT" ] && [ "$$BINARY_COMMIT" != "unknown" ]; then \
		RESOLVED=$$($(SRC_GIT) rev-parse --verify --quiet --end-of-options "$$BINARY_COMMIT^{commit}" 2>/dev/null); \
		if [ -z "$$RESOLVED" ]; then \
			echo "ERROR: installed binary reports commit $$BINARY_COMMIT, which does not exist in $(SRC_DIR)"; \
			echo "Its provenance cannot be checked against this repo, so forward-only cannot be proven."; \
			echo "Use SKIP_FORWARD_CHECK=1 to override (dangerous)."; \
			exit 1; \
		fi; \
		HEAD_COMMIT=$$($(SRC_GIT) rev-parse HEAD 2>/dev/null); \
		if [ "$$RESOLVED" = "$$HEAD_COMMIT" ]; then \
			echo "Binary is already at HEAD, nothing to do"; \
			exit 1; \
		fi; \
		if ! $(SRC_GIT) merge-base --is-ancestor "$$RESOLVED" HEAD 2>/dev/null; then \
			echo "ERROR: HEAD ($$($(SRC_GIT) rev-parse --short HEAD)) is NOT a descendant of installed binary ($$BINARY_COMMIT)"; \
			echo "This would be a DOWNGRADE. Refusing to rebuild."; \
			echo "Use SKIP_FORWARD_CHECK=1 to override (dangerous)."; \
			exit 1; \
		fi; \
		echo "Forward-only check passed: $$BINARY_COMMIT → $$($(SRC_GIT) rev-parse --short HEAD)"; \
	else \
		echo "Warning: cannot determine installed binary commit, skipping forward check"; \
	fi
endif

check-install-path:
	@resolved=$$(command -v $(BINARY) 2>/dev/null || true); \
	if [ "$$resolved" != "$(INSTALL_DIR)/$(BINARY)" ]; then \
		echo "Warning: $(BINARY) resolves to $${resolved:-nothing in PATH}, not $(INSTALL_DIR)/$(BINARY)"; \
		echo "  Add this before other PATH entries in your shell profile:"; \
		echo '  export PATH="$(INSTALL_DIR):$$PATH"'; \
	fi

install: check-up-to-date build
	@mkdir -p $(INSTALL_DIR)
	@rm -f $(INSTALL_DIR)/$(BINARY)
	@cp $(BUILD_DIR)/$(BINARY) $(INSTALL_DIR)/$(BINARY)
	@# Nuke any stale go-install binaries that shadow the canonical location
	@for bad in $(HOME)/go/bin/$(BINARY) $(HOME)/bin/$(BINARY); do \
		if [ -f "$$bad" ]; then \
			echo "Removing stale $$bad (use make install, not go install)"; \
			rm -f "$$bad"; \
		fi; \
	done
	@echo "Installed $(BINARY) to $(INSTALL_DIR)/$(BINARY)"
	@$(MAKE) --no-print-directory check-install-path
	@# Restart daemon so it picks up the new binary.
	@# A stale daemon is a recurring source of bugs (wrong session prefixes, etc.)
	@if $(INSTALL_DIR)/$(BINARY) daemon status >/dev/null 2>&1; then \
		echo "Restarting daemon to pick up new binary..."; \
		$(INSTALL_DIR)/$(BINARY) daemon stop >/dev/null 2>&1 || true; \
		sleep 1; \
		$(INSTALL_DIR)/$(BINARY) daemon start >/dev/null 2>&1 && \
			echo "Daemon restarted." || \
			echo "Warning: daemon restart failed (start manually with: gt daemon start)"; \
	fi
	@$(MAKE) --no-print-directory sync-plugins

# sync-plugins: Deploy this checkout's plugins into the town runtime directory.
#
# Plugins EXECUTE from <townRoot>/plugins/, never from this repo, so landing a
# plugin fix on main does not deploy it — this copy is the only thing that
# does. A fix split across Go and shell therefore deploys in two halves at two
# different times: the Go half rides the binary, the shell half rides nothing.
#
# It hangs off BOTH install paths deliberately. It used to hang off `install`
# alone — and `install` is the path a HUMAN takes. The only path that runs by
# itself is rebuild-gt's hourly `safe-install`, which had no sync at all, so
# the automated deploy path was the one with the hole in it. Measured
# 2026-08-26: seven plugins in the town differed from main and rebuild-gt's
# executing copy was dated 2026-08-02, missing both guards merged since.
#
# This does NOT deploy itself, and the reason is worth stating so nobody waits
# for it. rebuild-gt reaches this recipe only through `make safe-install` in the
# rig checkout, and `check-up-to-date` exits 1 whenever that checkout differs
# from its upstream. Measured 2026-08-26: the rig checkout was 12 commits behind
# origin/main, and the plugin copy executing in the town is the 2026-08-02 one,
# which predates the step that fast-forwards it. So the checkout cannot advance
# on its own, safe-install cannot complete, and this recipe is unreachable until
# someone fast-forwards that checkout once by hand. After that one bootstrap the
# loop closes: the sync below replaces the town's plugin copies with current
# ones, including rebuild-gt's, and every later cycle carries both halves.
#
# Failure warns rather than fails: the binary is already in place by the time
# this runs, and a non-zero exit here would report the install itself as broken.
# But the REASON is printed. It was previously sent to /dev/null under
# `|| true`, which made a sync that could not run look exactly like a sync that
# ran and found nothing to do.
sync-plugins:
	@if [ ! -x $(INSTALL_DIR)/$(BINARY) ]; then \
		echo "Warning: $(INSTALL_DIR)/$(BINARY) is not executable; plugins NOT synced" >&2; \
	elif out=$$($(INSTALL_DIR)/$(BINARY) plugin sync --source $(CURDIR)/plugins 2>&1); then \
		echo "$$out"; \
	else \
		echo "Warning: plugin sync failed; the town keeps executing the plugins it already had" >&2; \
		echo "$$out" | sed 's/^/  /' >&2; \
		echo "  Retry with: $(INSTALL_DIR)/$(BINARY) plugin sync --source $(CURDIR)/plugins" >&2; \
	fi

# safe-install: Replace binary WITHOUT restarting daemon or killing sessions.
# Use this for automated rebuilds (e.g., rebuild-gt plugin). Sessions pick up
# the new binary on their next natural cycle/handoff.
safe-install: check-up-to-date check-forward-only build
	@mkdir -p $(INSTALL_DIR)
	@# Atomic-ish replace: copy to temp then move (move is atomic on same filesystem)
	@cp $(BUILD_DIR)/$(BINARY) $(INSTALL_DIR)/$(BINARY).new
	@mv $(INSTALL_DIR)/$(BINARY).new $(INSTALL_DIR)/$(BINARY)
	@# Nuke any stale go-install binaries that shadow the canonical location
	@for bad in $(HOME)/go/bin/$(BINARY) $(HOME)/bin/$(BINARY); do \
		if [ -f "$$bad" ]; then \
			echo "Removing stale $$bad (use make install, not go install)"; \
			rm -f "$$bad"; \
		fi; \
	done
	@echo "Installed $(BINARY) to $(INSTALL_DIR)/$(BINARY) (daemon NOT restarted)"
	@$(MAKE) --no-print-directory check-install-path
	@# The binary is only half the deploy. Plugins run from the town, not from
	@# here, and this is the automated path — if it does not sync, nothing does.
	@$(MAKE) --no-print-directory sync-plugins
	@echo "Sessions will pick up new binary on next cycle."

# check-version-tag: Verify that if HEAD is tagged vX.Y.Z, the Version constant
# in internal/cmd/version.go equals X.Y.Z. No-op when HEAD is untagged, so it is
# safe to run on every build but only fails release tag checkouts.
# Prevents recurrence of gh#3459 (v0.13.0 shipped reporting 0.12.1).
check-version-tag:
	@TAG=$$(git describe --tags --exact-match HEAD 2>/dev/null || true); \
	if [ -z "$$TAG" ]; then \
		echo "check-version-tag: HEAD is not a release tag, skipping"; \
		exit 0; \
	fi; \
	case "$$TAG" in \
		v[0-9]*) TAG_VERSION=$${TAG#v} ;; \
		*) echo "check-version-tag: tag '$$TAG' is not a vX.Y.Z release tag, skipping"; exit 0 ;; \
	esac; \
	CODE_VERSION=$$(grep -E '^[[:space:]]*Version[[:space:]]*=[[:space:]]*"' internal/cmd/version.go | head -1 | sed 's/.*"\([^"]*\)".*/\1/'); \
	if [ -z "$$CODE_VERSION" ]; then \
		echo "ERROR: could not parse Version from internal/cmd/version.go"; \
		exit 1; \
	fi; \
	if [ "$$TAG_VERSION" != "$$CODE_VERSION" ]; then \
		echo "ERROR: version mismatch between git tag and Version constant"; \
		echo "  git tag at HEAD:          $$TAG (expects Version=$$TAG_VERSION)"; \
		echo "  internal/cmd/version.go:  Version=$$CODE_VERSION"; \
		echo ""; \
		echo "Run scripts/bump-version.sh before tagging, or re-tag HEAD correctly."; \
		echo "See gh#3459 for background."; \
		exit 1; \
	fi; \
	echo "check-version-tag: OK (tag $$TAG matches Version=$$CODE_VERSION)"

clean:
	rm -f $(BUILD_DIR)/$(BINARY)

test: test-makefile test-nested-modules
	go test ./...

# `go test ./...` stops at a module boundary: from the repo root it reports
# "matched no packages" for anything under a nested go.mod, so until this target
# existed plugins/dolt-snapshots was run by no gate at all. Each nested module
# gets its own line; internal/testenv's coverage check enumerates the modules
# that need one.
test-nested-modules:
	cd plugins/dolt-snapshots && go test ./...

test-makefile:
	bash scripts/check-install-path_test.sh
	bash scripts/sync-plugins_test.sh
	bash -n plugins/stuck-agent-dog/run.sh
	bash -n plugins/stuck-agent-dog/run_test.sh
	bash plugins/stuck-agent-dog/run_test.sh
	bash -n plugins/git-hygiene/run.sh
	bash -n plugins/git-hygiene/run_test.sh
	bash plugins/git-hygiene/run_test.sh
	bash -n plugins/gitignore-reconcile/run.sh
	bash -n plugins/submodule-commit/run.sh
	bash -n plugins/rebuild-gt/run.sh
	bash -n plugins/rebuild-gt/run_test.sh
	bash plugins/rebuild-gt/run_test.sh
	bash -n plugins/dolt-log-rotate/run.sh
	bash -n plugins/rig_repos_contract_test.sh
	bash plugins/rig_repos_contract_test.sh
	bash -n plugins/town_root_contract_test.sh
	bash plugins/town_root_contract_test.sh
	bash -n scripts/town-sweep.sh
	# Hermetic: builds its fixture under mktemp -d. Never pass --live-control here.
	bash scripts/town-sweep.sh --self-test

# Run e2e tests in isolated container (the only supported way to run them)
test-e2e-container:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -Command "$$max=$(E2E_BUILD_RETRIES); for($$i=1; $$i -le $$max; $$i++){ docker build $(E2E_BUILD_FLAGS) -f Dockerfile.e2e -t $(E2E_IMAGE) .; if($$LASTEXITCODE -eq 0){ break }; if($$i -eq $$max){ exit 1 }; Write-Host ('docker build failed (attempt ' + $$i + '), retrying...'); Start-Sleep -Seconds 2 }"
	@powershell -NoProfile -Command "$$max=$(E2E_RUN_RETRIES); for($$i=1; $$i -le $$max; $$i++){ docker run $(E2E_RUN_FLAGS) $(E2E_IMAGE); if($$LASTEXITCODE -eq 0){ break }; if($$i -eq $$max){ exit 1 }; Write-Host ('docker run failed (attempt ' + $$i + '), retrying...'); Start-Sleep -Seconds 2 }"
else
	@attempt=1; \
	while [ $$attempt -le $(E2E_BUILD_RETRIES) ]; do \
		docker build $(E2E_BUILD_FLAGS) -f Dockerfile.e2e -t $(E2E_IMAGE) . && break; \
		if [ $$attempt -eq $(E2E_BUILD_RETRIES) ]; then exit 1; fi; \
		echo "docker build failed (attempt $$attempt), retrying..."; \
		attempt=$$((attempt+1)); \
		sleep 2; \
	done
	@attempt=1; \
	while [ $$attempt -le $(E2E_RUN_RETRIES) ]; do \
		docker run $(E2E_RUN_FLAGS) $(E2E_IMAGE) && break; \
		if [ $$attempt -eq $(E2E_RUN_RETRIES) ]; then exit 1; fi; \
		echo "docker run failed (attempt $$attempt), retrying..."; \
		attempt=$$((attempt+1)); \
		sleep 2; \
	done
endif
