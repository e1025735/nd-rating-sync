# nd-rating-sync — Feature List

## Core purpose
Reads embedded star-rating tags from MP3, FLAC, Ogg-Vorbis, Opus, WAV, DSF,
M4A/AAC, and WMA files and writes them to Navidrome's user-rating system via
the Subsonic `setRating` API. Navidrome does not import embedded ratings on
its own; this plugin bridges file tags and the UI.

---

## Sync triggers

| Trigger | Details |
|---------|---------|
| Immediate on load | One-shot scan runs as soon as the plugin initialises |
| Recurring (cron) | Configurable cron expression, **default hourly**; idle runs are cheap (see *Change-detection gate*), so a frequent schedule is inexpensive |

---

## Incremental sync

After a successful scan, the timestamp is persisted per `(library, user)` in Navidrome's KV store. Subsequent runs skip any song whose file mtime predates that timestamp, so recurring scans are nearly instant after the first pass.

- Skip is decided by the matched file's modification time (captured while indexing the library mount) vs the stored threshold — file mtime is the right signal because tag editors update it the moment the user saves a rating, regardless of whether Navidrome's own library scan has caught up.
- KV failures are non-fatal: missing/corrupt state falls back to a full scan; failed writes mean the next run does redundant work, never incorrect work.
- Disable with `incremental_sync=false` to force a full scan every run (useful after changing a user's `ratingTagOrder`).

---

## Change-detection gate (skip unchanged libraries)

Incremental sync skips the per-*file* work, but a naive sweep still pages the entire `search3` listing every run. The change-detection gate eliminates that too: before paging a `(library, user)` pair, the plugin asks Navidrome for the library's `LastScanAt` (one `LibraryGetLibrary` metadata call). If Navidrome has **not** rescanned the library since our last completed sweep, the **whole pair is skipped** — no song paging at all. A fully-synced, unchanged library therefore costs ~one metadata call per library per run instead of hundreds of `search3` pages.

- The gate reuses the existing `last-synced` timestamp; no extra state. A gated skip never advances that timestamp, so when Navidrome eventually rescans, the per-file incremental path still re-processes exactly the files that changed.
- Fails **open**: any uncertainty (non-numeric library ID, host error, never-scanned library) falls back to paging — the gate can only *save* work, never skip work that should happen.
- Bypassed by `incremental_sync=false`, so a forced full scan always re-pages.
- Because it aligns the plugin's work to Navidrome's own scans, a tag edit on an *existing* file is applied after Navidrome's next library scan (run one pass with `incremental_sync=false` to pick it up immediately).

---

## Supported file formats

| Container | Tag system | File extensions |
|-----------|------------|-----------------|
| MP3 | ID3v2 (TXXX, POPM) | `.mp3` |
| FLAC | Vorbis comments | `.flac` |
| Ogg-Vorbis | Vorbis comments | `.ogg`, `.oga` |
| Opus | Vorbis comments (`OpusTags`) | `.opus` |
| WAV | ID3v2 chunk inside RIFF container | `.wav` |
| DSD Stream File | ID3v2 block (offset stored in DSD header) | `.dsf` |
| M4A / AAC | MP4 freeform (`----`) atoms | `.m4a`, `.aac`, `.mp4` |
| WMA | ASF Extended Content Description Object | `.wma` |

## Supported tag formats

`ratingTagOrder` values are *source applications*, not container-specific keys. Each source maps to whatever tag(s) that application writes in each container. Sources without a representation in a given container are silently skipped — they just never match.

| Key | MP3 / WAV / DSF (ID3v2) | FLAC / Ogg / Opus (Vorbis) | M4A / AAC (MP4 atom) | WMA (ASF) | Scale / notes |
|-----|-------------------------|----------------------------|----------------------|-----------|---------------|
| `WMP` | POPM ("windows media player") | — | — | `WM/SharedUserRating` WORD | Non-linear: 1/25/50/75/99 → 1–5 stars |
| `iTunes` | POPM ("itunes" / "com.apple.itunes") | — | `rating` freeform (lowercase) | — | Linear 0–100 in steps of 20 → 1–5 stars |
| `MediaMonkey` | TXXX `FMPS_Rating` | `FMPS_RATING` | `FMPS_Rating` freeform | `FMPS_Rating` Unicode string | Float 0.0–1.0, ceiling × 5 → 1–5 stars |
| `foobar2000` | TXXX `RATING` | `RATING` | `RATING` freeform (uppercase) | — | Integer 1–5 (0 / empty / out-of-range = unrated) |

---

## File location & filesystem access

A Navidrome plugin cannot open arbitrary paths, and the Subsonic API does not
give a plugin a real file path — the `path` field in a `search3` response is a
*synthesized* string (built from tags) by default, not a path that exists on
disk. So the plugin never opens it.

- Requires the manifest `library` permission with **`filesystem: true`**, plus the
  library assigned to the plugin in the Navidrome UI. Navidrome then read-only-mounts
  each library inside the sandbox and exposes the mount via `LibraryGetLibrary`.
- The plugin walks the mount and indexes every supported audio file by **(exact
  byte size, extension)**, then matches each Subsonic song to its file using the
  `size` Navidrome reports — no reliance on real paths.
- A song whose size matches no file, or matches more than one (an ambiguous
  size+extension collision), is treated as **unreadable** and skipped — never as
  "untagged" — so `clear_rating_if_untagged` can never wipe a rating for a file
  the plugin could not positively identify.
- Read-only: the plugin never writes to the music files.
- **File index is KV-cached across continuations.** Each chunk runs in a fresh
  WASM instance, so the recursive mount walk would otherwise repeat every
  callback — on slow filesystems that ate ~5–6 s of the 10 s budget per chunk.
  The first chunk persists the index to KV; subsequent chunks reload it,
  validated against Navidrome's `LastScanAt`. A rescanned library auto-rebuilds
  on the next chunk (stamp mismatch), so no explicit cache invalidation is
  needed. Index blobs above ~4 MiB are silently not persisted (sweep still
  works; just falls back to per-chunk walks).

---

## Per-user configuration

- **Tag priority order** — `ratingTagOrder` list; first tag *found in the file* wins
- **Skip already-rated** — songs with an existing Navidrome rating are left untouched (default on; can be disabled to allow overwrites)
- **Clear rating if untagged** — when enabled, songs whose file contains no recognised rating tag have their Navidrome rating removed (set to 0); requires `skip_already_rated=false` to also affect previously-rated songs. Files the plugin cannot read (I/O error, permission denied, unsupported extension, unparseable container) are **never** cleared — they're counted under `skipped_unreadable` so a transient read failure cannot wipe a user's rating.

---

## Robustness

- **Per-file size cap (64 MiB)** — files larger than the cap are not read into memory; they are counted under `skipped_unreadable` and never cleared. Guards the wasm sandbox against OOM on misreported paths.
- **Per-file panic isolation** — a panic in any container parser is recovered and the file is treated as unreadable, so a single hostile file cannot abort the whole sync run.
- **32-bit-safe chunk arithmetic** — RIFF (WAV) and ASF (WMA) chunk-size fields are evaluated in `uint64` before narrowing to `int`, so a sign-wrap on TinyGo wasip1 (32-bit `int`) cannot rewind the cursor and produce an infinite loop in the chunk walker.
- **Vorbis comment cap** — the FLAC / Ogg / Opus comment-block parser clamps the declared entry count to 1024 so a crafted file with `count = 2^32` cannot exhaust CPU/GC before the byte budget runs out.
- **KV key escaping** — `last-synced` keys URL-escape both `libraryID` and `username`, so a `:` inside either component cannot collide with a different `(library, user)` tuple.
- **Log-line hygiene** — every user-controlled string (paths, song IDs, library IDs, usernames, file extensions, server error messages) is rendered with `%q` so embedded `\r\n` or ANSI escapes cannot inject log lines into downstream aggregators.
- **No raw OS errors in open/read warnings** — failures to open or read a music file log only the path; the underlying `os` error string (which would distinguish "permission denied" from "no such file or directory") is gated to debug-level only, so plugin warnings cannot be used to probe arbitrary paths' existence via planted symlinks.

---

## Per-library configuration

- Multiple libraries supported; each library has its own user list
- Library scoped by Navidrome `libraryId` (the **numeric** ID); the same ID resolves the read-only filesystem mount, so a valid numeric ID is required (a blank/non-numeric ID is logged and skipped)

---

## Admin-level controls

| Setting | Default | Description |
|---------|---------|-------------|
| `sync_schedule` | `0 * * * *` | Cron expression for recurring sync (hourly default; idle runs are cheap via the change-detection gate, so a frequent schedule is fine) |
| `incremental_sync` | `true` | Skip files whose mtime predates the last successful scan; set false to force a full rescan every run |
| `dry_run` | `false` | Run the full scan pipeline without writing any ratings; logs `[DRY RUN] would_rate / would_clear` instead of calling `setRating`; does not advance the incremental-sync threshold |

---

## Subsonic API usage

- Paginates `search3` (500 songs/page) lazily — fetches only the page the current chunk needs, so a resumed sync re-reads at most one page
- Calls `setRating` once per song where a tag was found (idempotent)
- Reads `userRating` from the search response to implement skip-already-rated
- Reads `size` from the search response to locate each song's file under the library mount (Navidrome does not expose a real file path to plugins)
- Calls the `LibraryGetLibrary` host function once per library per run to read `LastScanAt` for the change-detection gate — skipping `search3` paging entirely when the library is unchanged

---

## Sync execution model

- Every scheduler callback is bound by Navidrome's hard **30s** plugin-call limit (force-closes the WASM module otherwise — not configurable by the plugin)
- Each callback processes songs for a **~20s budget** (`callBudget`), then stops
- If songs remain, the callback serialises a cursor — `(library index, user index, song offset, pair scan-start)` — into the payload of a fresh one-time callback (empty schedule ID → host mints a unique one) and returns; the continuation resumes exactly there
- Continuations are rescheduled with **zero delay**, which the host runs as `time.AfterFunc(0, …)` — so the next slice fires immediately. A large first import runs as a *continuous, back-to-back* chain of quick callbacks (`… time budget reached – rescheduled continuation …` → `sync complete`), completing in minutes. **Convergence speed is driven by this chain, not by the cron** — the cron interval only governs how often a *fresh* sweep starts
- **Overlap guard:** a full sweep records a short-lived heartbeat (`sweep-active` KV key) refreshed on every continuation; a freshly-triggered sweep skips if one is already running, so a frequent cron firing mid-import can't start a second concurrent chain. The heartbeat goes stale (≈2 min) so a crashed chain self-heals, and `OnInit` clears it on reload. Best-effort — `setRating` idempotency covers any residual race
- Progress lives only in the scheduler payload + KV store — package globals do **not** survive between callbacks (fresh WASM instance per call)
- The incremental-sync threshold for a `(library, user)` is saved only once that pair is fully processed, so an interrupted/continued sweep never advances the threshold past unprocessed songs

---

## What the plugin does NOT do

- Does not write tags back to files (read-only access to the filesystem)
- Does not support container formats beyond MP3, FLAC, Ogg-Vorbis, Opus, WAV, DSF, M4A/AAC, and WMA (AIFF, WavPack, DFF, etc. are skipped with a warning)
- Does not support tag formats beyond WMP POPM, iTunes POPM, MediaMonkey/foobar2000 FMPS_Rating, and foobar2000 RATING
- Does not import ratings in the other direction (Navidrome → file)
- Does not cache the Subsonic `search3` results between callbacks — each chunk fetches the page(s) it needs. A sweep of a *changed* library still enumerates it page by page (incremental sync skips the per-file work within); a sweep of an *unchanged* library is skipped wholesale by the change-detection gate, so it does no paging at all
