# nd-rating-sync – Navidrome plugin build
# Requires: TinyGo (https://tinygo.org/getting-started/install/)
#           zip

PLUGIN_NAME := nd-rating-sync
WASM        := plugin.wasm
PACKAGE     := $(PLUGIN_NAME).ndp

.PHONY: all build package clean

all: package

## build – compile to WebAssembly using TinyGo
build: $(WASM)

$(WASM): *.go
	tinygo build \
		-o $(WASM) \
		-target wasip1 \
		-buildmode=c-shared \
		-scheduler=none \
		-gc=conservative \
		.

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
