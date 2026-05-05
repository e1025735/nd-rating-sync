# nd-rating-sync — project context

Navidrome plugin (WASM) that reads embedded star-rating tags from MP3 files and writes them to Navidrome via the Subsonic `setRating` API. Navidrome doesn't import embedded ratings on its own; this plugin bridges file tags and the Navidrome user-rating system.

## File layout

| File | Role |
|------|------|
| `main.go` | Lifecycle (`nd_lifecycle_on_init`) and scheduler callback entry points |
| `scanner.go` | Config types/loading, user-triggered scan logic, `runSync`/`runSyncForUser`, Subsonic helpers |
| `id3.go` | ID3v2 tag parsing (`parseID3v2Rating`) — dispatches by per-user `tagOrder` |
| `rating.go` | Pure converters: `fmpsToStars`, `popmWMPToStars`, `popmITunesToStars`, `popmWinampToStars`, `popmLinear51ToStars` |
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
  - Each user: `username`, `trigger_user_scan`, `skip_already_rated` (default `true`), `ratingTagOrder`

## Supported tag formats (MP3 / ID3v2 only)

| `ratingTagOrder` key | ID3v2 frame | Scale |
|----------------------|-------------|-------|
| `"WMP"` | POPM (email contains "windows media player") | Non-linear fixed points (1/25/50/75/99) |
| `"iTunes"` | POPM (email contains "itunes" / "com.apple.itunes") | Linear 0–100 (20/40/60/80/100) |
| `"MediaMonkey"` | TXXX description "FMPS_Rating" | Float 0.0–1.0 → ceiling×5 |

Per-user `ratingTagOrder` controls priority; first match in the file wins.

## Scheduler IDs

| ID | Trigger |
|----|---------|
| `nd-rating-sync-recurring` | Cron-based full sync |
| `nd-rating-sync-immediate` | One-shot at plugin init |
| `nd-rating-sync-trigger-check` | Every 15 min — polls `trigger_user_scan=true` per user |
