# nd-rating-sync — project context

Navidrome plugin (WASM) that reads embedded star-rating tags from MP3, FLAC, Ogg-Vorbis, Opus, WAV, DSF, M4A/AAC and WMA files and writes them to Navidrome via the Subsonic `setRating` API. Navidrome doesn't import embedded ratings on its own; this plugin bridges file tags and the Navidrome user-rating system.

## File layout

| File | Responsibility |
|------|---------------|
| `main.go` | Entry points — lifecycle init, scheduler callback registration via `ratingPlugin` |
| `config.go` | Config types (`pluginConfig`, `libraryConfig`, `userConfig`) and `loadConfig()` |
| `scanner.go` | Sync orchestration — `runSync`, `runSyncForUser`, `checkAndRunUserTriggeredScan`, `extractStarsFromFile` returning a `fileReadResult` (`tagFound` / `tagAbsent` / `fileUnreadable`) so I/O failures, oversize files (`maxAudioFileBytes` = 64 MiB), unsupported extensions, and parser panics never trigger `clear_rating_if_untagged`. `readAudioFile` enforces the size cap; `dispatchParser` recovers panics from any container parser so one hostile file can't kill the whole sync. |
| `state.go` | Incremental-sync state — `loadLastSynced` / `saveLastSynced` backed by `host.KVStore`. KV key is `"last-synced:" + url.QueryEscape(libraryID) + ":" + url.QueryEscape(username)` so a `:` in either component can't collide with a different tuple. |
| `subsonic.go` | Subsonic API domain — response types, `fetchAllSongs`, `setRating` |
| `id3.go` | ID3v2 tag parsing (`parseID3v2Rating`) — dispatches by per-user `tagOrder` |
| `flac.go` | FLAC + Vorbis comment parsing (`parseFLACVorbisComments`, `parseFLACRating`) plus the shared `ratingFromVorbisComments` resolver — hand-rolled, no external dep. Comment count is clamped to `maxVorbisComments` (1024) so a crafted block declaring `count = 2^32` can't burn millions of allocations before the byte budget runs out. |
| `ogg.go` | Ogg page walker (`extractOggPackets`) and Vorbis/Opus comment dispatch (`parseOggVorbisRating`) — hand-rolled, no external dep |
| `wav.go` | WAV RIFF chunk walker (`parseWAVRating`) — extracts `id3 `/`ID3 ` chunk and delegates to `parseID3v2Rating`. Chunk-size arithmetic stays in `uint64` before narrowing to `int` so a high-bit-set `uint32` can't sign-wrap on 32-bit `wasip1` and rewind `pos` into an infinite loop (mirrors the wma.go pattern). |
| `dsf.go` | DSD Stream File parser (`parseDSFRating`) — reads ID3v2 offset from DSD header and delegates to `parseID3v2Rating` |
| `m4a.go` | MP4 atom walker (`walkAtoms`, `findAtom`, `parseM4ARating`) — resolves freeform `----` atoms for FMPS_Rating, RATING, and rating |
| `wma.go` | ASF header walker (`parseWMARating`, `parseASFExtContentDesc`, `decodeUTF16LE`) — reads `WM/SharedUserRating` and `FMPS_Rating` from Extended Content Description Object |
| `rating.go` | Pure converters: `fmpsToStars`, `ratingIntToStars`, `popmWMPToStars`, `popmITunesToStars` |
| `manifest.json` | Plugin metadata, `permissions`, and `config` (draft-07 JSON Schema + **JSONForms** `uiSchema`). Permission key is `library` (singular) + `filesystem:true`; capabilities are auto-detected from exported WASM functions, **not** declared here. See the manifest-format note under Build. |

## Build

Must be built with **TinyGo** targeting `wasip1`:

```
tinygo build -o plugin.wasm -target wasip1 -buildmode=c-shared .
zip -j nd-rating-sync.ndp manifest.json plugin.wasm
```

`go test ./...` and `go vet ./...` work on the regular toolchain (CI runs both). `pdk_stub.go` is `//go:build !wasip1` and provides no-op stand-ins for `logInfo`/`logDebug`/`logWarn`/`getConfig`; the Navidrome PDK ships matching non-wasip1 stubs for `host.*`. Only `GOARCH=wasm GOOS=wasip1 go vet ./...` fails — host imports have no Go function bodies under wasip1; TinyGo wires them up at link time. That part is expected.

### Manifest format (Navidrome ≥ v0.61)

`manifest.json` is validated against `plugins/manifest-schema.json` in the Navidrome module (top-level and per-permission `additionalProperties:false`). Navidrome's `ParseManifest` does a plain `json.Unmarshal`, so **unknown keys are silently dropped** — a typo means the feature just doesn't take effect. Gotchas (fixed across v0.9.1–v0.9.4):

- **Permissions:** the library permission key is `library` (singular) with `filesystem:true` for disk reads — *not* `libraries`, and there is no `allowWrite`. Wrong key ⇒ no filesystem access. Mirror the `library-inspector-rs` example.
- **No `capabilities` / `homepage` keys:** capabilities come from the exported functions registered in [main.go](main.go) (`lifecycle.Register`/`scheduler.Register`); the repo/URL field is `website`.
- **Config UI is JSONForms-Material 2.5 + ajv v6** (renderers in Navidrome's `ui/src/plugin/`; `uiSchema` is passed through untransformed). The `uiSchema` must use `VerticalLayout`/`Control` with `scope: "#/properties/<field>"` (object arrays — including nested ones — via `options.detail` + `elementLabelProp`; scopes inside a detail are relative to the array's *item* schema). A react-jsonschema-form root (`ui:widget`/`ui:placeholder`/`ui:enumNames`) has no valid `type`, so the **whole** Configuration section shows **"No applicable renderer found."** Working reference manifests: `discord-rich-presence-rs` (in-repo) and [kgarner7/navidrome-listenbrainz-daily-playlist](https://github.com/kgarner7/navidrome-listenbrainz-daily-playlist).
- **Stick to renderable constructs:** plain `boolean`/`integer`/`string`, nested object arrays, and `oneOf` of **string** `const`/`title`. Material has **no renderer/cell for `["type","null"]` union types or `const:null`** — avoid them.
- **Ordered vs multi-select arrays:** an enum array with `uniqueItems:true` renders as an **unordered** checkbox group (`MaterialEnumArrayRenderer`). For an **ordered** priority list (e.g. `ratingTagOrder`), omit `uniqueItems`, give `items` a primitive `type:"string"`+`enum`, and set the Control's `options.showSortButtons:true` so the list gets add/remove **and up/down** controls.
- **Tristate fields** (per-user `trigger_user_scan`/`skip_already_rated`/`clear_rating_if_untagged` + admin `default_*`) are a **string** `oneOf` (`"inherit"`/`"yes"`/`"no"`). They're plain `string` fields in [config.go](config.go), mapped to `*bool` by `parseTristateConfig` (`"inherit"`/empty/absent → nil), so `resolveTristate` is unchanged.
- **Reloading:** Navidrome caches the manifest (read from the `.ndp`). After editing `manifest.json` you must **restart Navidrome** (or set `Plugins.AutoReload=true`, or bump `version`/reinstall) for changes to show — otherwise the UI keeps rendering the *old* manifest. Confirm via the plugin's Manifest panel. A `config.go` change also requires rebuilding `plugin.wasm`.

Validate after edits: the manifest against `manifest-schema.json` and the inner `config.schema` as draft-07 (e.g. `npx ajv-cli@5 validate -s <schema> -d manifest.json --spec=draft2020 --strict=false`).

## Config model (v0.3.0+)

Config is a hierarchical JSON Schema (not a flat key-value list):

- Top-level admin scalars read via `pdk.GetConfig`: `sync_schedule`, `user_scan_cooldown_hours`, `max_songs_per_run`, `dry_run`
- Top-level admin tristate defaults (also via `pdk.GetConfig`): `default_trigger_user_scan`, `default_skip_already_rated`, `default_clear_rating_if_untagged` — the schema sends `"inherit"`/`"yes"`/`"no"`; `parseTristateConfig` maps them to a `*bool` in `pluginConfig` (`nil` = no admin default)
- `libraries` array read via `pdk.GetConfig("libraries")` as a JSON string, then unmarshaled:
  - Each library: `libraryId`, `libraryName`, `users[]`
  - Each user: `username`, `trigger_user_scan` / `skip_already_rated` / `clear_rating_if_untagged` (string `"inherit"`/`"yes"`/`"no"` in JSON, mapped to `*bool` by `parseTristateConfig`); resolved via `resolveTristate(user, adminDefault, pluginFallback)`, `ratingTagOrder`

## Supported tag formats

`ratingTagOrder` values are *source applications*, not container-specific keys. Each source maps to whatever tag(s) that application writes in each container. FLAC, Ogg-Vorbis and Opus all share the Vorbis comment format, so they use the same column.

| `ratingTagOrder` key | MP3 / WAV / DSF (ID3v2) | FLAC / Ogg / Opus (Vorbis) | M4A / AAC (MP4 atom) | WMA (ASF) | Scale |
|----------------------|-------------------------|----------------------------|-----------------------|-----------|-------|
| `"WMP"` | POPM ("windows media player") | — | — | `WM/SharedUserRating` WORD | Non-linear fixed points (1/25/50/75/99) |
| `"iTunes"` | POPM ("itunes" / "com.apple.itunes") | — | `rating` freeform (lowercase) | — | Linear 0–100 (20/40/60/80/100) |
| `"MediaMonkey"` | TXXX `FMPS_Rating` | `FMPS_RATING` | `FMPS_Rating` freeform | `FMPS_Rating` Unicode | Float 0.0–1.0 → ceiling×5 |
| `"foobar2000"` | TXXX `RATING` | `RATING` | `RATING` freeform (uppercase) | — | Integer 1–5 (0/empty = unrated) |

Per-user `ratingTagOrder` controls priority; first match in the file wins. Sources without a representation in a given container are silently skipped (e.g. `WMP` listed for a FLAC file simply never matches — keeping it in the order is harmless).

`ratingFromVorbisComments` (in `flac.go`) is the shared resolver used by the FLAC and Ogg/Opus paths — extending Vorbis-side tag detection means editing it once.

## Incremental sync

When `incremental_sync` is true (default), each (library, user) tuple records the wall-clock time of its last successful scan in the Navidrome KV store and skips songs whose file mtime predates it.

- KV key: `last-synced:{libraryID}:{username}` (plugin-scoped by the host).
- Value: scan-start timestamp (captured at function entry) as RFC3339Nano UTC.
- Skip rule: `os.Stat(path).ModTime().Before(threshold)` — exact-equality files re-process, which keeps `setRating` idempotent and avoids drift if mtime resolution is coarser than expected.
- Failure modes are non-fatal: a missing/malformed/unreadable KV value falls back to "no threshold" (full scan); a KV write failure means the next run does redundant work, never incorrect work.
- Set `incremental_sync=false` to force a full scan every run — useful after a user changes `ratingTagOrder`, since tag-order changes don't auto-invalidate the threshold.

## Scheduler IDs

| ID | Trigger |
|----|---------|
| `nd-rating-sync-recurring` | Cron-based full sync |
| `nd-rating-sync-immediate` | One-shot at plugin init |
| `nd-rating-sync-trigger-check` | Every 15 min — polls `trigger_user_scan=true` per user |
