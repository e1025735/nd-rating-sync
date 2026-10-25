# nd-rating-sync – Navidrome plugin build
# Requires: TinyGo (https://tinygo.org/getting-started/install/)
#           zip

PLUGIN_NAME := nd-rating-sync
WASM        := plugin.wasm
PACKAGE     := $(PLUGIN_NAME).ndp

.PHONY: all build package clean install

all: package

# Every non-test Go source, including internal/, so a change anywhere rebuilds.
GO_SOURCES := $(shell find . -name '*.go' -not -name '*_test.go') go.mod go.sum

## build – compile to WebAssembly using TinyGo (the same command as the
## release workflow, so local builds match released ones)
build: $(WASM)

$(WASM): $(GO_SOURCES)
	tinygo build -o $(WASM) -target wasip1 -buildmode=c-shared .

## package – bundle manifest + wasm into an .ndp archive
package: $(PACKAGE)

$(PACKAGE): manifest.json $(WASM)
	zip -j $@ manifest.json $(WASM)

## clean – remove build artefacts
clean:
	rm -f $(WASM) $(PACKAGE)

## install – copy .ndp to a local Navidrome plugins folder
#  Usage: make install PLUGINS_DIR=/data/navidrome/plugins
install: package
	@if [ -z "$(PLUGINS_DIR)" ]; then \
		echo "Usage: make install PLUGINS_DIR=/path/to/plugins"; exit 1; fi
	cp $(PACKAGE) $(PLUGINS_DIR)/
	@echo "Installed $(PACKAGE) → $(PLUGINS_DIR)/"
