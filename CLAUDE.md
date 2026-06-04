# nd-rating-sync — project context

Navidrome plugin (WASM) that reads embedded star-rating tags from MP3, FLAC, Ogg-Vorbis, Opus, WAV, DSF, M4A/AAC and WMA files and writes them to Navidrome via the Subsonic `setRating` API. Navidrome doesn't import embedded ratings on its own; this plugin bridges file tags and the Navidrome user-rating system.

## File layout

| File | Responsibility |
|------|---------------|
| `main.go` | Entry points — lifecycle init (`OnInit` clears the in-progress guard on load), scheduler callback registration, and `runSyncStep` / `runSyncStepUntil` (one budgeted slice of a sync; checks/refreshes the `sweep-active` overlap guard, reschedules a continuation when the budget is hit) via `ratingPlugin` |
| `config.go` | Config types (`pluginConfig`, `libraryConfig`, `userConfig`) and `loadConfig()` |
| `cursor.go` | `syncCursor` (resumable position carried in the scheduler payload), `parseCursor` / `marshal`, `callBudget` (10 s — see *Sync execution* for why this is much lower than Navidrome's hardcoded 30 s limit), and `deadlineCheckEvery` (1 — songs between deadline checks; raising it lets slow songs blow past both the 10 s budget and the host 30 s wall) |
| `scanner.go` | Sync engine — `runSyncChunk` (walks library/user pairs until the deadline; loads each pair's threshold and applies the **LastScanAt gate** — skips a fresh pair whose library has not been rescanned since the stored threshold, without saving), `processPairChunk` (one pair from a song offset, takes the threshold as a param, checks the deadline after each song), and `processSong`. `extractStarsFromFile` returns a `fileReadResult` (`tagFound` / `tagAbsent` / `fileUnreadable`) so I/O failures, oversize files (`maxAudioFileBytes` = 64 MiB), unsupported extensions, and parser panics never trigger `clear_rating_if_untagged`. `readAudioFile` enforces the size cap; `dispatchParser` recovers panics from any container parser so one hostile file can't kill the whole sync. |
| `paths.go` | Filesystem-mount resolution and file matching — `resolveMountPoint` (config `libraryId` → `host.LibraryGetLibrary(int32).MountPoint`), `buildFileIndex` (recursive `os.ReadDir` walk keyed by `sizeKey(size, ext)`), `matchFile` (song → file by exact size+suffix; ambiguous/missing → not found), plus the KV-backed file-index cache (`loadCachedIndex` / `saveCachedIndex`, key `"file-index:" + url.QueryEscape(libraryID)`, stamped with Navidrome's `LastScanAt` for validity, capped at `maxIndexBytes` = 4 MiB). See **File access** below. |
| `state.go` | KV-backed state — `loadLastSynced` / `saveLastSynced` (incremental threshold; KV key `"last-synced:" + url.QueryEscape(libraryID) + ":" + url.QueryEscape(username)` so a `:` in either component can't collide with a different tuple), plus the overlap-guard helpers `sweepInProgress` / `markSweepActive` / `clearSweepActive` (`sweep-active` heartbeat key, `sweepStaleAfter` = 2 min). All KV failures fail open. |
| `subsonic.go` | Subsonic API domain — response types (incl. `subsonicSong.Size`, used to locate the file under the mount), `fetchAllSongs`, `setRating` |
| `library.go` | `libraryLastScan` — wraps `host.LibraryGetLibrary` (empty libraryID → `LibraryGetAllLibraries`, newest `LastScanAt`) for the change-detection gate; returns `(zero,false)` on any uncertainty so the gate fails open. `cachedLibraryLastScan` memoises it per `runSyncChunk` call. |
| `id3.go` | ID3v2 tag parsing (`parseID3v2Rating`) — dispatches by per-user `tagOrder` |
| `flac.go` | FLAC + Vorbis comment parsing (`parseFLACVorbisComments`, `parseFLACRating`) plus the shared `ratingFromVorbisComments` resolver — hand-rolled, no external dep. Comment count is clamped to `maxVorbisComments` (1024) so a crafted block declaring `count = 2^32` can't burn millions of allocations before the byte budget runs out. |
| `ogg.go` | Ogg page walker (`extractOggPackets`) and Vorbis/Opus comment dispatch (`parseOggVorbisRating`) — hand-rolled, no external dep |
| `wav.go` | WAV RIFF chunk walker (`parseWAVRating`) — extracts `id3 `/`ID3 ` chunk and delegates to `parseID3v2Rating`. Chunk-size arithmetic stays in `uint64` before narrowing to `int` so a high-bit-set `uint32` can't sign-wrap on 32-bit `wasip1` and rewind `pos` into an infinite loop (mirrors the wma.go pattern). |
| `dsf.go` | DSD Stream File parser (`parseDSFRating`) — reads ID3v2 offset from DSD header and delegates to `parseID3v2Rating` |
| `m4a.go` | MP4 atom walker (`walkAtoms`, `findAtom`, `parseM4ARating`) — resolves freeform `----` atoms for FMPS_Rating, RATING, and rating |
| `wma.go` | ASF header walker (`parseWMARating`, `parseASFExtContentDesc`, `decodeUTF16LE`) — reads `WM/SharedUserRating` and `FMPS_Rating` from Extended Content Description Object |
| `rating.go` | Pure converters: `fmpsToStars`, `ratingIntToStars`, `popmWMPToStars`, `popmITunesToStars` |
| `manifest.json` | Plugin metadata, capabilities, permissions (`library` + `filesystem:true` for read-only file access — NOT the invalid `libraries`/`allowWrite` shape), JSON Schema config definition |

## Build

Must be built with **TinyGo** targeting `wasip1`:

```
tinygo build -o plugin.wasm -target wasip1 -buildmode=c-shared .
zip -j nd-rating-sync.ndp manifest.json plugin.wasm
```

`go test ./...` and `go vet ./...` work on the regular toolchain (CI runs both). `pdk_stub.go` is `//go:build !wasip1` and provides no-op stand-ins for `logInfo`/`logDebug`/`logWarn`/`getConfig`; the Navidrome PDK ships matching non-wasip1 stubs for `host.*` (incl. `host.LibraryMock` for `LibraryGetLibrary`). Only `GOARCH=wasm GOOS=wasip1 go vet ./...` fails — host imports have no Go function bodies under wasip1; TinyGo wires them up at link time. That part is expected.

## File access (filesystem mount)

Navidrome does **not** hand a plugin an openable path. The Subsonic `search3`
`path` field is a *synthesized fake* (`AlbumArtist/Album/NN-NN - Title.ext`, see
Navidrome's `server/subsonic/helpers.go::fakePath`) unless the plugin's player
has Report Real Path enabled — which a plugin cannot set (no manifest field; the
`subsonicapi` permission can't modify player settings). And even a real path is
the *host* path, not the in-sandbox one. So the plugin ignores `s.Path` and reads
files through the library **mount** instead:

- `manifest.json` declares `"library": { "filesystem": true }`. Navidrome then
  read-only-mounts each assigned library at `/libraries/{id}` inside the sandbox.
- `resolveMountPoint(libraryId)` → `host.LibraryGetLibrary(int32(id)).MountPoint`.
  `libraryId` must be numeric; a blank/non-numeric ID or empty `MountPoint`
  (filesystem perm not granted / library unassigned) is logged and the
  (library, user) is skipped without touching any rating.
- `buildFileIndex` walks the mount with `os.ReadDir` recursion (the pattern used
  by the official `library-inspector` / `artist-nfo` plugins; avoids any
  `filepath.WalkDir` TinyGo edge cases) and indexes by `sizeKey(size, ext)`.
- `matchFile` resolves a song to its file by exact `(size, suffix)`. 0 or >1
  candidates → not found → caller counts `skippedUnreadable` (never `tagAbsent`),
  so an unmatched/ambiguous file can never be cleared by `clear_rating_if_untagged`.
- There is **no** host "read file" API; `readAudioFile` uses plain `os.Open` on
  the matched mount path.

### File-index cache (KV-backed)

Each scheduler callback is a fresh WASM instance, so `buildFileIndex` would
otherwise re-walk the mount on every chunk — on slow filesystems that has
been observed to eat 5–6 s of the 10 s `callBudget` per chunk, choking
throughput. To avoid this, the first chunk of a sweep persists the index to
KV; subsequent chunks read it back and skip the walk.

- KV key: `"file-index:" + url.QueryEscape(libraryID)`. Value: a
  `cachedFileIndexBlob` (`{v, l, e[]}` — version, `LastScanAt` at build time,
  flat entry list) JSON-marshalled. The bucket map is reconstructed on
  load from each entry's size+ext, so it isn't stored.
- Validity stamp: Navidrome's `LastScanAt`. On every chunk, `resolveAndIndex`
  loads the blob and accepts it only if `blob.LastScanAt == current.LastScanAt`.
  A mismatch (Navidrome rescanned mid-sweep, or this is a fresh sweep after
  a real scan) triggers a rebuild and an overwrite — no explicit invalidation
  needed, no "delete on sweep complete" book-keeping.
- LastScanAt unknown (0 / host error / non-numeric ID) → no read attempt,
  no persist. We cannot validate without it, so caching would just leak
  stale data.
- Size cap: `maxIndexBytes` = 4 MiB. Above that the blob is silently dropped
  (debug-logged) and the next chunk rebuilds — better than blowing the KV
  with a runaway library.
- Fail-open everywhere (KV error, parse error, version mismatch, oversize
  blob, missing scan stamp): the cache can only make things faster, never
  less correct.

## Config model (v0.3.0+)

Config is a hierarchical JSON Schema (not a flat key-value list):

- Top-level admin scalars read via `pdk.GetConfig`: `sync_schedule` (defaults to hourly `0 * * * *` — idle runs are cheap thanks to the LastScanAt gate, so a frequent schedule is fine), `incremental_sync`, `dry_run` (there is no song cap — chunking + the `callBudget` keep runs safe; see **Sync execution**)
- Top-level admin tristate defaults (also via `pdk.GetConfig`): `default_skip_already_rated`, `default_clear_rating_if_untagged` — each a `*bool` in `pluginConfig`; `nil` = no admin default
- `libraries` array read via `pdk.GetConfig("libraries")` as a JSON string, then unmarshaled:
  - Each library: `libraryId`, `libraryName`, `users[]`
  - Each user: `username`, `skip_already_rated` / `clear_rating_if_untagged` (both tristate `*bool`); resolved via `resolveTristate(user, adminDefault, pluginFallback)`, `ratingTagOrder`

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
- Value: scan-start timestamp, captured when the pair *starts* (carried as `syncCursor.PairStart` across continuations) and written only when the pair is **fully** processed — so an interrupted/resumed sweep never advances the threshold past songs it hasn't reached yet. RFC3339Nano UTC.
- Skip rule: the matched file's `ModTime().Before(threshold)` (mtime captured by `buildFileIndex` while walking the mount, not a separate `os.Stat`) — exact-equality files re-process, which keeps `setRating` idempotent and avoids drift if mtime resolution is coarser than expected.
- Failure modes are non-fatal: a missing/malformed/unreadable KV value falls back to "no threshold" (full scan); a KV write failure means the next run does redundant work, never incorrect work.
- Set `incremental_sync=false` to force a full scan every run — useful after a user changes `ratingTagOrder`, since tag-order changes don't auto-invalidate the threshold.

### LastScanAt gate (skip unchanged libraries)

Incremental sync skips per-*file* work but still pages the whole `search3` listing. The gate (in `runSyncChunk`, helper in `library.go`) eliminates the listing too: when starting a **fresh** pair, it compares the library's `LastScanAt` (`host.LibraryGetLibrary`, unix seconds) against the stored `last-synced` threshold and, if `lastScan.Before(threshold)` (Navidrome hasn't rescanned since our last sweep), **skips the entire pair** with no paging.

- Gate condition: `cur.Offset == 0 && cur.PairStart == "" && cfg.IncrementalSync && !threshold.IsZero()`. `incremental_sync=false` bypasses it.
- A gated skip **must not save** the threshold — it stays pinned to the last *real* sweep, so when Navidrome eventually rescans, the per-file mtime path still re-processes the files that changed. Reuses the existing `last-synced` value; both `LastScanAt` and `PairStart` are host-clock so comparable. No new KV key.
- `libraryLastScan` fails **open** (returns `(zero,false)` → page) on a non-numeric library ID, host error, or `LastScanAt==0`. Library IDs must be numeric (Navidrome's internal IDs are) for the gate to engage. `cachedLibraryLastScan` memoises per call so N users of one library cost one host call.
- Requires the `library` permission (also what grants the filesystem reads) — the host registers the Library service only when `Permissions.Library != nil`.

## Scheduler IDs

| ID | Trigger |
|----|---------|
| `nd-rating-sync-recurring` | Cron-based full sync |
| `nd-rating-sync-immediate` | One-shot at plugin init |
| *(host-minted, empty ID)* | Sync continuation — scheduled by `runSyncStep` when `callBudget` is hit, payload carries the `syncCursor`. `OnCallback` routes every schedule ID into `runSyncStep`. |

## Sync execution (30s host limit)

Navidrome force-closes any plugin call exceeding **30 s** (`extism.Manifest.Timeout`, hardcoded in the host's `manager_loader.go`; not configurable by the plugin). So no callback may do unbounded work:

- `runSyncStep` runs one `callBudget` (**10 s** — much lower than the 30 s host limit; see *Why so low* below) slice via `runSyncChunk`, then either finishes (`sync complete`) or reschedules a one-time continuation carrying the `syncCursor` and returns. The body is wrapped in `recover()` so a Go-side panic returns a clean error to the host instead of dropping the WASM module.
- The continuation uses an **empty** `scheduleID` so the host mints a unique one — reusing the firing entry's own ID would be rejected as a duplicate (the one-time entry is still registered during its own callback).
- **Each callback is a fresh WASM instance** — Go package globals do NOT persist across calls. All cross-call state must live in the scheduler payload (`syncCursor`) or the KV store.
- Forward progress is guaranteed: `processPairChunk` checks the deadline every `deadlineCheckEvery` (1) song, so every callback advances the cursor by ≥1 song; a continuation chain always terminates. `setRating` idempotency makes a re-processed song on resume harmless.
- **Why callBudget is 10 s, not 20 s.** Production saw a wazero host-side panic (nil pointer deref in `Context.Walltime`) ~16 s into a callback, then later a 30 s host timeout (`module closed with context deadline exceeded`) when a chunk ran the full wall. Both root in the same thing: chunks were running too long. The 10 s budget keeps us well below any plausible host timeout. Combined with `deadlineCheckEvery=1`, this is enforced tightly: even with 2–3 s slow songs we yield within one song of the budget, never approaching the 30 s wall.
- **Continuations fire immediately.** `SchedulerScheduleOneTime(0, …)` runs as `time.AfterFunc(0, …)` host-side, so the next slice starts at once — a big first import is a *continuous back-to-back chain* (minutes), **not** gated by the cron. The cron interval only governs how often a *fresh* sweep starts; convergence speed comes from the chain.
- **Overlap guard.** Every sweep records/refreshes the `sweep-active` heartbeat (`markSweepActive`) on every callback; a *fresh* sweep (`!resumed`) bows out via `sweepInProgress()` if a heartbeat is younger than `sweepStaleAfter` (2 min; a future-dated heartbeat from a backward clock step is clamped to "stale" too). On `done` it calls `clearSweepActive`; `OnInit` also clears it (a reload kills the chain). Continuations refresh but never run the in-progress check (so a chain can't block itself). Best-effort (no KV CAS) — idempotency covers the residual fresh-vs-fresh race.
