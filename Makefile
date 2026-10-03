# make setup builds foreman, installs it, and copies the default stage instructions
# and routing file.
BIN_DIR ?= $(HOME)/.local/bin
# Foreman reads global stage files from stages/ next to its config file.
CONFIG_DIR ?= $(if $(FOREMAN_CONFIG),$(dir $(FOREMAN_CONFIG)),$(HOME)/.config/foreman)
STAGES_DIR := $(CONFIG_DIR:/=)/stages

.PHONY: setup build install config test

setup: install config

build:
	go build -o dist/foreman ./cmd/foreman

install: build
	mkdir -p $(BIN_DIR)
	@# Replace the file instead of overwriting it; macOS kills a binary rewritten in place.
	rm -f $(BIN_DIR)/foreman
	cp dist/foreman $(BIN_DIR)/foreman

# Existing stage and routing files are yours, so config never overwrites them. It
# reports each stage file that differs from its default, with a command to replace it.
config:
	mkdir -p "$(STAGES_DIR)"
	@sh scripts/install-stages.sh "$(CURDIR)/defaults/stages" "$(STAGES_DIR)"
	@dest="$(CONFIG_DIR:/=)/routing.yaml"; \
	if [ -e "$$dest" ]; then echo "kept $$dest"; \
	else cp defaults/routing.yaml "$$dest" && echo "wrote $$dest"; fi

test:
	go vet ./... && go test ./...
	sh scripts/install-stages_test.sh
