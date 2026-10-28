module github.com/e1025735/nd-rating-sync

// TinyGo supports a range of Go versions; when raising this, check that the
// TinyGo release used by the release workflow supports it.
go 1.25

require (
	github.com/bogem/id3v2/v2 v2.1.4
	github.com/extism/go-pdk v1.1.3
	github.com/navidrome/navidrome/plugins/pdk/go v0.0.0-20260707115439-6c95a66ad6e4
	github.com/stretchr/testify v1.12.1
)

require (
	github.com/stretchr/objx v0.5.3 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/text v0.3.8 // indirect
)

// ── LOCAL DEVELOPMENT ──────────────────────────────────────────────────────────
// When developing against a local Navidrome checkout, uncomment the replace
// directive below and point it at your clone:
//
// replace github.com/navidrome/navidrome/plugins/pdk/go => /path/to/navidrome/plugins/pdk/go
//
// For releases, use the exact pseudo-version from `go list -m` inside the
// Navidrome source tree, or wait for the PDK to be published as a standalone
// module.
