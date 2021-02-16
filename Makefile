BIN          := logwrap
PLATFORM_BIN  = $(BIN)$(if $(findstring windows,$(1)),.exe)
EXE           = $(if $(findstring windows,$(1)),.exe)
ARCH          = $(shell go env GOARCH)
OS            = $(shell go env GOOS)
ARCHLIST     ?= x86 x64
OSLIST       ?= linux freebsd netbsd macos windows
SKIPLIST      = macos-x86 # removed in go1.15
PLATFORMS    := $(filter-out $(SKIPLIST),$(foreach os,$(OSLIST),$(foreach arch,$(ARCHLIST),$(os)-$(arch))))
BRANCH        = $(shell git rev-parse --abbrev-ref HEAD)
COMMIT        = $(shell git rev-parse --short HEAD)
PREFIX       ?= /usr/local
BINDIR       := $(PREFIX)/bin
BASH          = $(shell which bash)
STATICCHECK   = $(shell go env GOPATH)/bin/staticcheck
VERSION      ?= dev

ifdef CI
VERSION := ci
endif

empty =
space = $(empty) $(empty)
comma = ,

.DEFAULT_GOAL := $(BIN)

SUBDIRS = dist build
.PHONY: $(SUBDIRS)
$(SUBDIRS): %: ; @mkdir -p $*

.PHONY: clean
clean:
	rm -rf $(BIN) $(SUBDIRS)

.PHONY: test
test: ; go test ./...

.PHONY: check
check: checks := inherit
check: checks += -ST1005 # incorrectly formatted error strings
check: checks := $(subst $(space),$(comma),$(strip $(checks)))
check:
	go vet ./...
	$(STATICCHECK) -checks $(checks) ./...

.PHONY: install
install: $(BIN)
	install -Dm755 $(BIN) -t $(DESTDIR)$(BINDIR)

$(BIN): build/$(OS)-$(ARCH)
	cp $< $(BIN)$(call EXE,$<)

build/%: ldflags  = -X main.version=$(VERSION)
build/%: SHELL   := $(BASH)
build/%: OS       = $(shell $(call canonic_os,$(firstword $(call split,$(@F)))))
build/%: ARCH     = $(shell $(call canonic_arch,$(lastword $(call split,$(@F)))))
build/%: *.go | build
	@env GOOS=$(OS) GOARCH=$(ARCH) go build -ldflags "$(ldflags)" -o $@
	@echo build: $(@F)

.PHONY: $(PLATFORMS)
.SECONDEXPANSION:
$(PLATFORMS): | dist/$(BIN)-$(VERSION)-$$@$$(call EXE,$$@)

dist/$(BIN)-$(VERSION)-%: | build/% dist
	@cp build/$* $@
	@cd $(@D) && sha256sum $(@F) > $(@F).sha256
	@echo dist: $(@F)

.PHONY: release
release: private SHELL := $(BASH)
release: pristine clean check test $(PLATFORMS)
	@$(call release-files)
	@$(call confirm,Push v$(VERSION)?,\
		echo 'Release v$(VERSION) cancelled')
	@$(call release-message) | hub release create \
		--draft \
		--browse \
		--file - \
		$$(echo dist/* | sed 's,dist/,--attach &,g') \
		v$(VERSION)
	@$(call confirm,Publish v$(VERSION)?,\
		hub release delete v$(VERSION); \
		echo 'Release v$(VERSION) cancelled')
	@hub release edit --draft=false --message "" v$(VERSION)
	@echo publish v$(VERSION): OK

.PHONY: pristine
pristine:
	@git diff-index --quiet $(BRANCH) || { \
		echo 'git: commit all changes before proceeding'; \
		exit 1; \
	}

split = $(subst -, ,$(1))

define canonic_arch
    case $(1) in
        x86) echo 386 ;;
        x64) echo amd64 ;;
    esac
endef

define canonic_os
    case $(1) in
        macos) echo darwin ;;
            *) echo $(1) ;;
    esac
endef

define confirm
$(call require-bash,confirm); \
read -s -n 1 -p "$(1) [yN] " yn; echo $$yn; \
if [[ $${yn,,} != y ]]; then \
	$(if $(2),$(strip $(2));) \
	echo 'Aborting...'; \
	exit 1; \
fi
endef

define require-bash
test $(SHELL) = $(BASH) || { echo "'$(1)' requires bash"; exit 1; }
endef

define release-message
{ echo v$(VERSION); echo; kc --show $(VERSION) | tail -n +3; }
endef

define release-files
{ \
echo --- v$(VERSION) ---; \
du -h -c dist/* | sed 's/dist\///'; \
echo ---; \
}
endef
