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
| `wav.go` | WAV RIFF chunk walker (`parseWAVRating`) — extracts `id3 `/`ID3 ` chunk and delegates to `parseID3v2Rating` |
| `dsf.go` | DSD Stream File parser (`parseDSFRating`) — reads ID3v2 offset from DSD header and delegates to `parseID3v2Rating` |
| `m4a.go` | MP4 atom walker (`walkAtoms`, `findAtom`, `parseM4ARating`) — resolves freeform `----` atoms for FMPS_Rating, RATING, and rating |
| `wma.go` | ASF header walker (`parseWMARating`, `parseASFExtContentDesc`, `decodeUTF16LE`) — reads `WM/SharedUserRating` and `FMPS_Rating` from Extended Content Description Object |
| `rating.go` | Pure converters: `fmpsToStars`, `ratingIntToStars`, `popmWMPToStars`, `popmITunesToStars` |
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

- Top-level admin scalars read via `pdk.GetConfig`: `sync_schedule`, `user_scan_cooldown_hours`, `max_songs_per_run`, `dry_run`
- `libraries` array read via `pdk.GetConfig("libraries")` as a JSON string, then unmarshaled:
  - Each library: `libraryId`, `libraryName`, `users[]`
  - Each user: `username`, `trigger_user_scan`, `skip_already_rated` (default `true`), `clear_rating_if_untagged` (default `false`), `ratingTagOrder`

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
