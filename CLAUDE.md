# nd-rating-sync — project context

Navidrome plugin (WASM) that reads embedded star-rating tags from MP3, FLAC, Ogg-Vorbis, Opus, WAV, DSF, M4A/AAC and WMA files and writes them to Navidrome via the Subsonic `setRating` API. Navidrome doesn't import embedded ratings on its own; this plugin bridges file tags and the Navidrome user-rating system.

## File layout

| File | Responsibility |
|------|---------------|
| `main.go` | Entry points — lifecycle init (`OnInit` clears the in-progress guard on load, checks the file-index config hash and registers the schedules) and the scheduler callback, which hands every schedule ID to `scanner.RunSyncStep` via `ratingPlugin` |
| `config.go` | `loadConfig()` / `loadConfigFrom()` — parses the PDK config into `scanner.PluginConfig` (the root `pluginConfig` / `libraryConfig` / `userConfig` names are aliases of the scanner types, so the config hash and the sync engine always see exactly what was loaded) |
| `internal/scanner/types.go` | Config and cursor types, `SyncCursor` (resumable position carried in the scheduler payload, incl. `Retry`) with `parseCursor` / `marshal` / `nextPair`, `callBudget` (15 s — see *Sync execution*), `maxPairAttempts` / `retryDelaySeconds`, `RunSyncStep` / `runSyncStepUntil` (one budgeted slice of a sync; checks/refreshes the `sweep-active` overlap guard, reschedules a continuation when the budget is hit), `FileCacheConfig` (the settings the persistent file index depends on) and `pairFingerprint` (the per-pair settings stored with the incremental threshold) |
| `internal/scanner/sync.go` | `runSyncChunk` (walks the (library, user) pairs, LastScanAt gate, file index, retry/skip of pairs that make no progress), `ensureLibraryIndexed` (persistent index build/reuse), `processPairChunk` (pages `search3`, `deadlineCheckEvery` = 1 song between deadline checks — raising it lets slow songs blow past the budget and the host's 30 s wall), the per-chunk tally and `logKVUsage` |
| `internal/scanner/song.go` | Per-song pipeline `processSong` (skip-already-rated, file match, incremental skip with the new-song override `songAddedSince`, read, `setRating`) |
| `internal/scanner/metadata.go` | `extractStarsFromFile` → `readAudioMetadata` (`media.ExtractMetadata`) → `parseStars` (`media.ParseRating`). Any open/read/parse error is `FileUnreadable`, never `TagAbsent`. |
| `internal/scanner/filesystem_index.go`, `filesystem_match.go`, `filesystem_scan.go` | Filesystem-mount resolution, indexing and file matching — `resolveMountPoint` (config `libraryId` → `host.LibraryGetLibrary(int32).MountPoint`), `buildFileIndexWithoutCache` / `walkAudioFiles` (recursive `os.ReadDir` walk keyed by `sizeKey(size, ext)`; a walk cut off by the deadline returns `errWalkDeadline` and is never used partially), `cachedFileIndex`, `matchFile` / `matchFileInBucketCache` (song → file by exact size+suffix; ambiguous/missing → not found), and the persistent index build `scanLibraryChunk`. See **File access** below. |
| `internal/scanner/library.go` | `libraryLastScan` — wraps `host.LibraryGetLibrary` for the change-detection gate; returns `(zero,false)` on any uncertainty (blank/non-numeric ID, host error, never scanned) so the gate fails open. `cachedLibraryLastScan` memoises it per `runSyncChunk` call. |
| `internal/adapter/kv_store/kv_store.go` | KV-backed state — `LoadLastSynced` / `SaveLastSynced` (incremental threshold plus pair fingerprint; KV key `"last-synced:" + url.QueryEscape(libraryID) + ":" + url.QueryEscape(username)` so a `:` in either component can't collide with a different tuple), the config-hash check `ConfigHashFor` / `EnsureConfigCacheIsCurrent` (see *Config-change invalidation*), the persistent index (`ScanState`, `LoadBucket` / `SaveBucket`, `DeleteLibraryBuckets`, `LoadLibraryScanState` / `SaveLibraryScanState`), `GetPercentageKVStorageUsage`, plus the overlap-guard helpers `SweepInProgress` / `MarkSweepActive` / `ClearSweepActive` (`sweep-active` heartbeat key, `SweepStaleAfter` = 2 min). All KV failures fail open. |
| `internal/adapter/subsonic/subsonic.go` | Subsonic API domain — response types (incl. `SubsonicSong.Size`, used to locate the file under the mount, and `Created`, used by the new-song override), `FetchSongPage`, `SetRating` (query-escapes the song ID and username: the host parses the URI with `url.Parse` and Navidrome allows any character in a username) |
| `internal/*/logging_wasip1.go`, `logging_stub.go` | Per-package log functions: `pdk.Log` under `wasip1`, no-ops in native builds (tests) |
| `internal/media/exports.go` | The media package's API: `ExtractMetadata(ext, f)` and `ParseRating(ext, data, path, tagOrder)`. `ParseRating` returns an error for a corrupt or unsupported tag, which the scanner reports as unreadable. Each format's `parseXRating` is a two-value wrapper of its error-returning `ratingFromX`. |
| `internal/media/id3.go` | ID3v2 tag parsing (`parseID3v2Rating` / `ratingFromID3v2`, `id3v2SyncsafeSize`) and the partial-read extractor `extractID3v2Metadata` — reads the 10-byte header, takes the syncsafe tag size, reads exactly that many bytes straight into one buffer and stops. A tag that starts with "ID3" but has a broken size, exceeds `maxMetadataReadBytes` or is cut off is an error, as is a tag the library cannot parse (e.g. ID3v2.2). `readID3v2TagAt` (DSF) can reserve leading bytes so the container header is written in place without a second copy. Parsing is limited to `TXXX`/`POPM` (`ParseFrames`), so cover art and other large frames are skipped rather than decoded. |
| `internal/media/flac.go` | FLAC + Vorbis comment parsing (`parseFLACVorbisComments`, `parseFLACRating` / `ratingFromFLAC`, `ratingFromVorbisComments`) plus the partial-read extractor `extractFLACMetadata` — walks 4-byte metadata block headers and `Seek`s past non-VORBIS_COMMENT bodies (PICTURE blocks can be many MiB) so we never load the audio frames; metadata that ends before a block flagged as last is an error. `parseVorbisCommentBlock` works on sub-slices (no per-comment copy), compares every length as `uint64` (a `uint32` ≥ 2^31 would wrap negative as an `int` on 32-bit `wasip1`, pass the bounds check and trigger a multi-GiB allocation that TinyGo cannot recover from), and drops `METADATA_BLOCK_PICTURE` cover art. A corrupt comment count is bounded by the bytes available, since each entry needs at least its 4-byte length. |
| `internal/media/ogg.go` | Ogg page walker (`extractOggPackets`) and Vorbis/Opus comment dispatch (`parseOggVorbisRating` / `ratingFromOgg`) plus the streaming extractor `extractOggMetadata` — `oggPacketReader` follows the lacing values page by page and reads only the identification and comment packets; comment entries above `oggMaxKeptComment` (4 KiB, i.e. `METADATA_BLOCK_PICTURE` cover art that Opus keeps inside the comment packet) are `Seek`ed past unread, and `oggSynth` rebuilds a compact Ogg stream. A cut-off or malformed header packet is an error (fileUnreadable), never tagAbsent. |
| `internal/media/wav.go` | WAV RIFF chunk walker (`parseWAVRating` / `ratingFromWAV`) plus the partial-read extractor `extractWAVMetadata` — walks chunk headers and `Seek`s past audio (`data`/`fmt `) chunks; synthesises a minimal RIFF/WAVE container holding only the `id3 ` chunk. Chunk-size arithmetic stays in `uint64` before narrowing to `int` so a high-bit-set `uint32` can't sign-wrap on 32-bit `wasip1`. |
| `internal/media/dsf.go` | DSD Stream File parser (`parseDSFRating` / `ratingFromDSF`) plus the partial-read extractor `extractDSFMetadata` — reads the 28-byte DSD header, follows the embedded ID3 offset (which can sit GiB into the file past all the DSD samples) and reads only the tag. |
| `internal/media/m4a.go` | MP4 atom walker (`walkAtoms`, `findAtom`, `parseM4ARating`) plus the partial-read extractor `extractM4AMetadata` — walks top-level atom headers and `Seek`s past `mdat` (audio data, often hundreds of MiB), then walks the `moov` children and `Seek`s past `trak` (sample tables, several MiB on long tracks) via `extractMoovUdta`, returning a synth `moov` that holds only `udta` in a single allocation — reading the whole `moov` per file exhausted the WASM heap on large full sweeps. Handles atoms with `size==0` (extends to EOF) and `size==1` (extended 64-bit size). |
| `internal/media/wma.go` | ASF header walker (`parseWMARating`, `parseASFExtContentDesc`, `decodeUTF16LE`) plus the partial-read extractor `extractWMAMetadata` — walks ASF object headers and `Seek`s past non-ECDO objects (notably the ASF Data Object that holds the audio). Returns a synth Header Object with the ECDO as the only child. |
| `internal/media/rating.go` | Pure converters: `fmpsToStars`, `ratingIntToStars`, `ratingMusicBeeToStars`, `popmWMPToStars`, `popmITunesToStars`, `popmMusicBeeToStars`, `popmMediaMonkeyToStars`, `sharedUserRatingToStars` |
| `internal/media/constants.go` | `maxMetadataReadBytes` (16 MiB) — the per-file cap on what an extractor pulls into memory |
| `manifest.json` | Plugin metadata, capabilities, permissions (`library` + `filesystem:true` for read-only file access — NOT the invalid `libraries`/`allowWrite` shape), JSON Schema config definition |

## Build

Must be built with **TinyGo** targeting `wasip1`:

```
tinygo build -o plugin.wasm -target wasip1 -buildmode=c-shared .
zip -j nd-rating-sync.ndp manifest.json plugin.wasm
```

`make build`, the release workflow and the Renovate bump workflow all run this same command (`make` also rebuilds on any change under `internal/`). Keep them in sync.

- `-gc` is left at the target default (`conservative`). `leaking` would never free memory and a full sweep would run out of heap.
- `-panic` stays at the default `abort`, which prints the panic message (`trap` prints nothing). Panics cannot be recovered on WebAssembly anyway, so the message is the only clue.

`go test ./...` and `go vet ./...` work on the regular toolchain (CI runs both). `pdk_stub.go` and each package's `logging_stub.go` are `//go:build !wasip1` and provide no-op stand-ins for the log functions and `getConfig`; the Navidrome PDK ships matching non-wasip1 stubs for `host.*` (incl. `host.LibraryMock` for `LibraryGetLibrary`). Only `GOARCH=wasm GOOS=wasip1 go vet ./...` fails — host imports have no Go function bodies under wasip1; TinyGo wires them up at link time. That part is expected.

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
- `buildFileIndexWithoutCache` walks the mount with `os.ReadDir` recursion (the
  pattern used by the official `library-inspector` / `artist-nfo` plugins; avoids
  any `filepath.WalkDir` TinyGo edge cases) and indexes by `sizeKey(size, ext)`.
  Without the persistent index this walk repeats in every callback; a walk that
  takes more than a third of `callBudget` logs a hint to enable
  `cache_libraries_filesystem_tree`, and a walk that never fits in a budget is
  retried `maxPairAttempts` times and then skipped (see *Sync execution*).
- With `cache_libraries_filesystem_tree` the index lives in KV buckets
  (`bucket:{libraryId}:{size}:{ext}` → `[]FileRecord`), built over several
  callbacks by `scanLibraryChunk`. `ScanState.PendingDirs` is a FIFO queue whose
  head is the directory being read; `ResumeAfter` is the last entry of it already
  indexed (`os.ReadDir` sorts by name), so a build cut off by the deadline resumes
  inside the directory. A finished index is reused until Navidrome's `LastScanAt`
  passes the one stored in the state; every new build first deletes the
  library's buckets (`DeleteLibraryBuckets`), so records of moved or deleted
  files cannot make a song ambiguous. Records store whole-second mtimes; the
  match reports the end of that second so a same-second change is re-read.
- `matchFile` resolves a song to its file by exact `(size, suffix)`. 0 or >1
  candidates → not found → caller counts `skippedUnreadable` (never `tagAbsent`),
  so an unmatched/ambiguous file can never be cleared by `clear_rating_if_untagged`.
- There is **no** host "read file" API; `readAudioMetadata` uses plain `os.Open`
  on the matched mount path.

## Config model (v0.3.0+)

Config is a hierarchical JSON Schema (not a flat key-value list):

- Top-level admin scalars read via `pdk.GetConfig`: `sync_schedule` (defaults to hourly `0 * * * *` — idle runs are cheap thanks to the LastScanAt gate, so a frequent schedule is fine), `incremental_sync`, `dry_run` (there is no song cap — chunking + the `callBudget` keep runs safe; see **Sync execution**), `cache_libraries_filesystem_tree` (persistent file index in KV) and `kv_storage_max_size` (must match `permissions.kvstore.maxSize`; only used to report KV usage, with a warning at 90 %)
- Top-level admin tristate defaults (also via `pdk.GetConfig`): `default_skip_already_rated`, `default_clear_rating_if_untagged` — each a `*bool` in `pluginConfig`; `nil` = no admin default
- `libraries` array read via `pdk.GetConfig("libraries")` as a JSON string, then unmarshaled:
  - Each library: `libraryId`, `libraryName`, `users[]`
  - Each user: `username`, `skip_already_rated` / `clear_rating_if_untagged` (both tristate `*bool`); resolved via `resolveTristate(user, adminDefault, pluginFallback)`, `ratingTagOrder`

### Config-change invalidation

Two independent mechanisms, each keyed on only the settings its state depends on:

- **Persistent file index.** The plugin stores a SHA256 `config-hash` of `scanner.FileCacheConfig(cfg)` — the `cache_libraries_filesystem_tree` switch and the list of library IDs, nothing else — and compares it on startup and before each sync callback. If it differs, it purges the `bucket`, `libraryState` and `config` prefixes via `KVStoreDeleteByPrefix` and writes the new hash. Editing a schedule, `dry_run` or a user's settings does not touch the index.
- **Incremental thresholds** are never purged by a config change. Each `last-synced` value carries a fingerprint of its own pair's `ratingTagOrder`, `skip_already_rated` and `clear_rating_if_untagged` (`pairFingerprint`); a threshold saved with other settings reads as "none", so that pair's next sweep reads every file once. Values written by older versions have no fingerprint and are accepted as they are.

## Supported tag formats

`ratingTagOrder` values are *source applications*, not container-specific keys. Each source maps to whatever tag(s) that application writes in each container. FLAC, Ogg-Vorbis and Opus all share the Vorbis comment format, so they use the same column.

| `ratingTagOrder` key | MP3 / WAV / DSF (ID3v2) | FLAC / Ogg / Opus (Vorbis) | M4A / AAC (MP4 atom) | WMA (ASF) | Scale |
|----------------------|-------------------------|----------------------------|-----------------------|-----------|-------|
| `"WMP"` | POPM ("windows media player") | — | — | `WM/SharedUserRating` DWORD (WORD also accepted) | POPM: non-linear fixed points (1/64/128/196/255). WMA: 0–99 (WMP writes 1/25/50/75/99), ranges 1–12/13–37/38–62/63–87/88–99 → 1–5 stars; >99 = unrated |
| `"iTunes"` | POPM ("itunes" / "com.apple.itunes") | — | `rating` freeform (lowercase) | — | Linear 0–100 (20/40/60/80/100) |
| `"MediaMonkey"` | POPM ("no@email", MediaMonkey's own frame), else TXXX `FMPS_Rating` | `RATING` (0–100), else `FMPS_RATING` | `rate` freeform (any case), else `FMPS_Rating` freeform | `FMPS_Rating` Unicode | POPM: exact whole-star bytes 1/64/128/196/255 (+ MediaMonkey 3.x 23/252); `RATING`/`rate`: 20/40/60/80/100; FMPS: whole stars only (0.2 steps) |
| `"foobar2000"` | TXXX `RATING` | `RATING` | `RATING` freeform (uppercase) | — | Integer 1–5 (0/empty = unrated) |
| `"MusicBee"` | POPM ("musicbee", exact whole-star bytes 1/64/128/196/255) | `RATING` | `RATE` freeform (any case) | `WM/SharedUserRating` (same as WMP) | `0/20/40/60/80/100` → 1–5 stars; half-star values (10/30/…/90) are treated as unrated because Navidrome has no half stars |

Per-user `ratingTagOrder` controls priority; first match in the file wins. Sources without a representation in a given container are silently skipped (e.g. `WMP` listed for a FLAC file simply never matches — keeping it in the order is harmless).

Half stars are never synced (Navidrome has none): MusicBee/MediaMonkey POPM half-star bytes (13/54/118/186/242), 0–100 values not on a multiple of 20, and FMPS values not on a 0.2 step are all reported as unrated. MusicBee/MediaMonkey POPM must be decoded by exact value, not by range — their ½★ byte (13) is larger than their 1★ byte (1). WMP and iTunes POPM keep range decoding. MediaMonkey's own field wins over an `FMPS_Rating` in the same file; in WMA MediaMonkey is only read from `FMPS_Rating` (its native WMA field is unverified).

`ratingFromVorbisComments` (in `flac.go`) is the shared resolver used by the FLAC and Ogg/Opus paths — extending Vorbis-side tag detection means editing it once.

## Incremental sync

When `incremental_sync` is true (default), each (library, user) tuple records the wall-clock time of its last successful scan in the Navidrome KV store and skips songs whose file mtime predates it.

- KV key: `last-synced:{libraryID}:{username}` (plugin-scoped by the host).
- Value: scan-start timestamp plus the pair fingerprint (`"<RFC3339Nano> <fingerprint>"`, see *Config-change invalidation*), the timestamp captured when the pair *starts* (carried as `SyncCursor.PairStart` across continuations) and written only when the pair is **fully** processed — so an interrupted/resumed sweep never advances the threshold past songs it hasn't reached yet. RFC3339Nano UTC.
- Skip rule: the matched file's `ModTime().Before(threshold)` (mtime captured by `buildFileIndex` while walking the mount, not a separate `os.Stat`) — exact-equality files re-process, which keeps `setRating` idempotent and avoids drift if mtime resolution is coarser than expected.
- New-song override: a song whose Subsonic `created` is at/after the threshold is processed even with an old mtime (`songAddedSince` in `song.go`). A renamed/moved file keeps its mtime but Navidrome imports it as a new, unrated song; without this it would be skipped forever. A missing/unparseable `created` falls back to the plain mtime rule.
- Failure modes are non-fatal: a missing/malformed/unreadable KV value falls back to "no threshold" (full scan); a KV write failure means the next run does redundant work, never incorrect work.
- Changing a user's `ratingTagOrder`, `skip_already_rated` or `clear_rating_if_untagged` invalidates that pair's threshold automatically (fingerprint). Set `incremental_sync=false` to force a full scan every run, e.g. for debugging.

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
| *(host-minted, empty ID)* | Sync continuation — scheduled by `RunSyncStep` when `callBudget` is hit (or, delayed, after a failed attempt), payload carries the `SyncCursor`. `OnCallback` routes every schedule ID into `RunSyncStep`. |

## Sync execution (30s host limit)

Navidrome force-closes any plugin call exceeding **30 s** (`extism.Manifest.Timeout`, hardcoded in the host's `manager_loader.go`; not configurable by the plugin). So no callback may do unbounded work:

- `RunSyncStep` runs one `callBudget` (**15 s** — well below the 30 s host limit; see *Why 15 s* below) slice via `runSyncChunk`, then either finishes (`sync complete`) or reschedules a one-time continuation carrying the `SyncCursor` and returns.
- **No panic recovery in production.** TinyGo cannot `recover()` on WebAssembly: a panic exits the module without running deferred functions (seen by the host as `module closed`). The `recover()` in `parseStars` only protects native builds (`go test`, fuzzing). Code on the sync path must therefore be panic-free by construction — bounds-check every length, compare untrusted sizes as `uint64`, never index without a length check; the media parsers are fuzzed for this.
- The continuation uses an **empty** `scheduleID` so the host mints a unique one — reusing the firing entry's own ID would be rejected as a duplicate (the one-time entry is still registered during its own callback).
- **Each callback is a fresh WASM instance** — Go package globals do NOT persist across calls. All cross-call state must live in the scheduler payload (`SyncCursor`) or the KV store.
- Forward progress is guaranteed: `processPairChunk` checks the deadline every `deadlineCheckEvery` (1) song, so every callback that reaches a pair's songs advances the cursor by ≥1 song; a persistent index build indexes ≥1 directory entry per directory read and saves its progress. `setRating` idempotency makes a re-processed song on resume harmless.
- **Pairs that make no progress are bounded.** A failed `search3` page, or a mount walk (without the persistent index) that does not finish within a whole budget, counts an attempt in `SyncCursor.Retry`. Such a continuation is scheduled after `retryDelaySeconds` (30 s, well under `SweepStaleAfter` so the chain still counts as active) instead of immediately; after `maxPairAttempts` (3) the pair is skipped until the next sweep, without saving its threshold. Any processed song resets the counter. A walk that runs out of budget only because earlier pairs used it up is not counted. Without this, a username Navidrome does not know or a mount too slow to walk re-ran back to back forever and blocked every cron sweep through the heartbeat.
- **Why 15 s.** Production saw a wazero host-side panic (nil pointer deref in `Context.Walltime`) ~16 s into a callback, then later a 30 s host timeout (`module closed with context deadline exceeded`) when a chunk ran the full wall. Both rooted in chunks running too long. The budget was cut to 10 s and later raised to 15 s (#23); combined with `deadlineCheckEvery=1` it is enforced tightly — even with 2–3 s slow songs we yield within one song of the budget, never approaching the 30 s wall.
- **Continuations fire immediately** (except retries, see above). `SchedulerScheduleOneTime(0, …)` runs as `time.AfterFunc(0, …)` host-side, so the next slice starts at once — a big first import is a *continuous back-to-back chain* (minutes), **not** gated by the cron. The cron interval only governs how often a *fresh* sweep starts; convergence speed comes from the chain.
- **Overlap guard.** Every sweep records/refreshes the `sweep-active` heartbeat (`MarkSweepActive`) on every callback; a *fresh* sweep (`!resumed`) bows out via `SweepInProgress()` if a heartbeat is younger than `SweepStaleAfter` (2 min; a future-dated heartbeat from a backward clock step is clamped to "stale" too). On `done` it calls `ClearSweepActive`; `OnInit` also clears it (a reload kills the chain). Continuations refresh but never run the in-progress check (so a chain can't block itself). Best-effort (no KV CAS) — idempotency covers the residual fresh-vs-fresh race.
