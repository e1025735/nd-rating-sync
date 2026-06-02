module github.com/yourusername/nd-rating-sync

// TinyGo targets a specific Go version; check your TinyGo release notes
// and align this with the supported version (typically 1.22+).
go 1.25

require (
	github.com/bogem/id3v2/v2 v2.1.4
	github.com/extism/go-pdk v1.1.3
	github.com/navidrome/navidrome/plugins/pdk/go v0.0.0-20260602123857-dad4203f9a93
	github.com/stretchr/testify v1.11.1
)

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/stretchr/objx v0.5.2 // indirect
	golang.org/x/text v0.3.8 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
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
