# nd-rating-sync — project context

Navidrome plugin (WASM) that reads embedded star-rating tags from MP3, FLAC, Ogg-Vorbis and Opus files and writes them to Navidrome via the Subsonic `setRating` API. Navidrome doesn't import embedded ratings on its own; this plugin bridges file tags and the Navidrome user-rating system.

## File layout

| File | Responsibility |
|------|---------------|
| `main.go` | Entry points — lifecycle init, scheduler callback registration via `ratingPlugin` |
| `config.go` | Config types (`pluginConfig`, `libraryConfig`, `userConfig`) and `loadConfig()` |
| `scanner.go` | Sync orchestration — `runSync`, `runSyncForUser`, `checkAndRunUserTriggeredScan`, `extractStarsFromFile` |
| `state.go` | Incremental-sync state — `loadLastSynced` / `saveLastSynced` backed by `host.KVStore` |
| `subsonic.go` | Subsonic API domain — response types, `fetchAllSongs`, `setRating` |
| `id3.go` | ID3v2 tag parsing (`parseID3v2Rating`) — dispatches by per-user `tagOrder` |
| `flac.go` | FLAC + Vorbis comment parsing (`parseFLACVorbisComments`, `parseFLACRating`) plus the shared `ratingFromVorbisComments` resolver — hand-rolled, no external dep |
| `ogg.go` | Ogg page walker (`extractOggPackets`) and Vorbis/Opus comment dispatch (`parseOggVorbisRating`) — hand-rolled, no external dep |
| `rating.go` | Pure converters: `fmpsToStars`, `ratingIntToStars`, `popmWMPToStars`, `popmITunesToStars`, `popmWinampToStars`, `popmLinear51ToStars` |
| `manifest.json` | Plugin metadata, capabilities, JSON Schema config definition |

## Build

Must be built with **TinyGo** targeting `wasip1`:

```
tinygo build -o plugin.wasm -target wasip1 -buildmode=c-shared .
zip -j nd-rating-sync.ndp manifest.json plugin.wasm
```

Standard `go build` / `go vet` always fail with "missing function body" from the extism PDK's WASM host imports — this is expected and not a code error. `GOARCH=wasm GOOS=wasip1 go vet ./...` will additionally error on `host.RegisterLifecycle` (Navidrome PDK is TinyGo-only) — also expected.

## Config model (v0.3.0+)

Config is a hierarchical JSON Schema (not a flat key-value list):

- Top-level admin scalars read via `pdk.GetConfig`: `sync_schedule`, `user_scan_cooldown_hours`, `max_songs_per_run`
- `libraries` array read via `pdk.GetConfig("libraries")` as a JSON string, then unmarshaled:
  - Each library: `libraryId`, `libraryName`, `users[]`
  - Each user: `username`, `trigger_user_scan`, `skip_already_rated` (default `true`), `clear_rating_if_untagged` (default `false`), `ratingTagOrder`

## Supported tag formats

`ratingTagOrder` values are *source applications*, not container-specific keys. Each source maps to whatever tag(s) that application writes in each container. FLAC, Ogg-Vorbis and Opus all share the Vorbis comment format, so they use the same column.

| `ratingTagOrder` key | MP3 (ID3v2) | FLAC / Ogg / Opus (Vorbis comments) | Scale |
|----------------------|-------------|-------------------------------------|-------|
| `"WMP"` | POPM (email contains "windows media player") | — | Non-linear fixed points (1/25/50/75/99) |
| `"iTunes"` | POPM (email contains "itunes" / "com.apple.itunes") | — | Linear 0–100 (20/40/60/80/100) |
| `"MediaMonkey"` | TXXX description "FMPS_Rating" | `FMPS_RATING` | Float 0.0–1.0 → ceiling×5 |
| `"foobar2000"` | TXXX description "RATING" | `RATING` | Integer 1–5 (0/empty = unrated) |

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
