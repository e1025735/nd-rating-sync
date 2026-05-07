# Contributing to nd-rating-sync

Thanks for looking into the code. This document covers everything you need to
build, test, and extend the plugin.

---

## Repository layout

```
main.go          – lifecycle init and scheduler callback (entry points only)
config.go        – pluginConfig / libraryConfig / userConfig types + loadConfig()
scanner.go       – sync orchestration: runSync, runSyncForUser, checkAndRunUserTriggeredScan
subsonic.go      – Subsonic API types and helpers: fetchAllSongs, setRating
id3.go           – ID3v2 tag parsing: reads frames once, picks winner by tagOrder
rating.go        – pure star converters: fmpsToStars, popmWMPToStars, popmITunesToStars
pdk_stub.go      – build-tag stub (!wasip1): no-op log helpers + getConfig, so tests
                   compile without TinyGo or any WASM toolchain
```

Test files sit next to the code they cover (`rating_test.go`, `config_test.go`,
etc.). `testhelpers_test.go` has the shared `mapGetter` helper.

---

## Prerequisites

| Tool | Purpose |
|------|---------|
| Go 1.22+ | Running tests and `go vet` |
| [TinyGo](https://tinygo.org/getting-started/install/) | Building the WASM plugin (`tinygo build`) |
| `zip` | Packaging the `.ndp` file |

You do **not** need TinyGo to run the test suite — only to produce a deployable
plugin binary.

---

## Running the tests

```sh
go test ./...
```

Because `pdk_stub.go` is guarded with `//go:build !wasip1`, the regular Go
toolchain can compile and test everything without any WASM setup. The PDK's
host-import functions are replaced by no-op stubs.

### Fuzz targets

Three fuzz targets live in `fuzz_test.go`:

```sh
go test -fuzz=FuzzFmpsToStars       -fuzztime=30s
go test -fuzz=FuzzPopmConverters    -fuzztime=30s
go test -fuzz=FuzzParseID3v2Rating  -fuzztime=30s
```

The corpora are seeded with representative values; the fuzzer looks for panics
and out-of-range star counts.

---

## Building the plugin

```sh
tinygo build -o plugin.wasm -target wasip1 -buildmode=c-shared .
zip -j nd-rating-sync.ndp manifest.json plugin.wasm
```

Drop `nd-rating-sync.ndp` into your Navidrome plugins folder to test end-to-end.

---

## How the DI seam works

The PDK's `pdk.GetConfig` and `pdk.Log` are WASM host imports that don't exist
in a standard Go build. To keep tests fast and toolchain-free, two thin layers
hide the WASM boundary:

**Logging** — `pdk_stub.go` defines `logInfo / logDebug / logWarn` as no-ops
when building without the `wasip1` tag. The real `pdk.go` (TinyGo only) wires
them to `pdk.Log`.

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
3. Add the format key to the `switch` in `parseID3v2Rating` in `id3.go` —
   populate the `found` map in the frame-scan section, not the tagOrder loop.
4. Add the new key to the `enum` in `manifest.json` under
   `libraries.items.users.items.ratingTagOrder.items`.
5. Update `FEATURES.md` and `README.md`.

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
