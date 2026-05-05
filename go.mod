module github.com/yourusername/nd-rating-sync

// TinyGo targets a specific Go version; check your TinyGo release notes
// and align this with the supported version (typically 1.22+).
go 1.23

require (
	github.com/bogem/id3v2/v2 v2.1.4
	github.com/extism/go-pdk v1.0.2
	github.com/navidrome/navidrome/plugins/pdk/go v0.0.0-20260101000000-000000000000
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
