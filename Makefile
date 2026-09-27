# make setup builds foreman, installs it, and copies the default stage instructions.
BIN_DIR ?= $(HOME)/.local/bin
# Foreman reads global stage files from stages/ next to its config file (D48).
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

# Existing stage files are yours, so config never overwrites them.
config:
	mkdir -p $(STAGES_DIR)
	@for f in defaults/stages/*.md; do \
		dest=$(STAGES_DIR)/$$(basename $$f); \
		if [ -e "$$dest" ]; then echo "kept $$dest"; \
		else cp "$$f" "$$dest" && echo "wrote $$dest"; fi; \
	done

test:
	go vet ./... && go test ./...
