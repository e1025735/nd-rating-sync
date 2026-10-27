# Contributing to nd-rating-sync

Thanks for looking into the code. This document covers everything you need to
build, test, and extend the plugin.

---

## Repository layout

```
main.go          – lifecycle init and scheduler callback (entry points only)
config.go        – loadConfig(); the config types are aliases of the scanner's
internal/scanner/ – sync engine: `types.go` holds the config/cursor types and `RunSyncStep`, `sync.go` the chunk processing (pairs, retries), `song.go` the per-song pipeline (`processSong`), `metadata.go` `extractStarsFromFile` / `readAudioMetadata` / `parseStars`, `locate.go` song → file through Navidrome's matcher, `library.go` the per-callback host lookups (`hostLookups`: library, mount point, LastScanAt gate, granted users and libraries).
internal/adapter/subsonic/ – Subsonic API adapter: `FetchSongPage`, `SetRating`, and Subsonic response types
internal/adapter/kv_store/ – KV state: incremental thresholds, overlap guard, clean-up of the old file index
internal/media/  – container extractors and tag parsers; `exports.go` (`ExtractMetadata`, `ParseRating`, `Supported`, and the `formats` table of extensions) is the package API, `rating.go` the pure star converters
internal/logging/ – `Info` / `Warn` / `Debug` / `Trace`, filtered by the host log level before they reach the host
pdk_stub.go, internal/logging/logging_stub.go – build-tag stubs (!wasip1): getConfig + no-op log functions, so tests
                   compile without TinyGo or any WASM toolchain
```

Test files sit next to the code they cover (`rating_test.go`, `config_test.go`,
etc.). Shared helpers live in `test_support_test.go` (root and `internal/scanner`)
and `testhelpers_test.go` (`internal/media`); `mapGetter` is in `config_test.go`.

---

## Prerequisites

| Tool | Purpose |
|------|---------|
| Go 1.25+ (see `go.mod`) | Running tests and `go vet` |
| [TinyGo](https://tinygo.org/getting-started/install/) | Building the WASM plugin (`tinygo build`) |
| `zip` | Packaging the `.ndp` file |

You do **not** need TinyGo to run the test suite — only to produce a deployable
plugin binary.

---

## Running the tests

```sh
go test ./...
```

Because `pdk_stub.go` and `internal/logging/logging_stub.go` are guarded with
`//go:build !wasip1`, the regular Go toolchain can compile and test everything
without any WASM setup. The Navidrome PDK ships non-wasip1 mocks for its host
functions (`host.SubsonicAPIMock`, `host.KVStoreMock`, `host.LibraryMock`,
`host.MatcherMock`, …), which the tests set up per case.

CI also runs the tests as `GOARCH=386`, so size arithmetic that only overflows
with a 32-bit `int` (as on TinyGo wasip1) is caught:

```sh
GOARCH=386 go test ./...
```

### Fuzz targets

The fuzz targets live in `internal/media/fuzz_test.go`. Run one at a time:

```sh
go test ./internal/media -run=^$ -fuzz=FuzzFmpsToStars      -fuzztime=30s
go test ./internal/media -run=^$ -fuzz=FuzzPopmConverters   -fuzztime=30s
go test ./internal/media -run=^$ -fuzz=FuzzParseID3v2Rating -fuzztime=30s
go test ./internal/media -run=^$ -fuzz=FuzzParseWAVRating   -fuzztime=30s
go test ./internal/media -run=^$ -fuzz=FuzzParseDSFRating   -fuzztime=30s
go test ./internal/media -run=^$ -fuzz=FuzzParseM4ARating   -fuzztime=30s
go test ./internal/media -run=^$ -fuzz=FuzzParseWMARating   -fuzztime=30s
```

The corpora are seeded with representative values; the fuzzer looks for panics
and out-of-range star counts.

---

## Building the plugin

```sh
tinygo build -o plugin.wasm -target wasip1 -buildmode=c-shared .
zip -j nd-rating-sync.ndp manifest.json plugin.wasm
```

`make package` does the same.

Drop `nd-rating-sync.ndp` into your Navidrome plugins folder to test end-to-end.

---

## How the DI seam works

The PDK's `pdk.GetConfig` and `pdk.Log` are WASM host imports that don't exist
in a standard Go build. To keep tests fast and toolchain-free, two thin layers
hide the WASM boundary:

**Logging** — `internal/logging` defines `Info / Warn / Debug / Trace`. Built
without the `wasip1` tag (`logging_stub.go`) they are no-ops; the `wasip1`
build sends them to `pdk.Log`.

**Config** — `loadConfig()` delegates to `loadConfigFrom(get configGetter)`,
where `configGetter` is just `func(key string) (string, bool)`. Production
passes `getConfig` (backed by the real PDK); tests pass a `mapGetter` closure
over a local `map[string]string`, so there is no global state and tests run in
parallel safely.

**Scheduler / lifecycle** — `main.go` exposes `registerSchedules(cfg)` so
the schedule registration logic can be tested without calling the actual host
functions.

If you add a new PDK call, follow the same pattern: put the real call behind
an injected function or a build-tag stub, and test with a fake.

---

## Adding a new tag format

1. Add a new converter in `rating.go` (pure function, no PDK dependency).
2. Write unit tests in `rating_test.go` covering boundary values.
3. Record the new source in each container's parser (`ratingFromID3v2`,
   `ratingFromVorbisComments`, `ratingFromM4A`, `parseASFExtContentDesc`) with
   `r.set` / `r.parsed` on the `ratings` collector while the tags are walked;
   `r.pick` applies the user's `ratingTagOrder` at the end.
4. Add the new key to the `enum` in `manifest.json` under
   `libraries.items.users.items.ratingTagOrder.items`.
5. Update the source tables in `README.md`, `FEATURES.md`, `DETAILS.md` and
   `CLAUDE.md`.

---

## Commit style

Follow the existing `<type>: <subject>` convention:

```
feat:     new user-visible behaviour
fix:      bug fix
refactor: restructuring without behaviour change
docs:     documentation only
test:     tests only
```

Keep the subject line under 72 characters. Put the *why* in the body, not the
*what* — the diff already shows what changed.
