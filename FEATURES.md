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
| Recurring (cron) | Configurable cron expression, default every 6 hours |
| User-triggered | `trigger_user_scan="yes"` flag checked every 15 min; one-shot scan per user |
| Cooldown guard | Configurable minimum gap between user-triggered scans (default 24 h) |

---

## Incremental sync

After a successful scan, the timestamp is persisted per `(library, user)` in Navidrome's KV store. Subsequent runs skip any song whose file mtime predates that timestamp, so recurring scans are nearly instant after the first pass.

- Skip is decided by `os.Stat(path).ModTime()` vs the stored threshold — file mtime is the right signal because tag editors update it the moment the user saves a rating, regardless of whether Navidrome's own library scan has caught up.
- KV failures are non-fatal: missing/corrupt state falls back to a full scan; failed writes mean the next run does redundant work, never incorrect work.
- Disable with `incremental_sync=false` to force a full scan every run (useful after changing a user's `ratingTagOrder`).

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

## Per-user configuration

- **Tag priority order** — `ratingTagOrder` list; first tag *found in the file* wins
- **Skip already-rated** — songs with an existing Navidrome rating are left untouched (default on; can be disabled to allow overwrites)
- **Clear rating if untagged** — when enabled, songs whose file contains no recognised rating tag have their Navidrome rating removed (set to 0); requires `skip_already_rated="no"` to also affect previously-rated songs. Files the plugin cannot read (I/O error, permission denied, unsupported extension, unparseable container) are **never** cleared — they're counted under `skipped_unreadable` so a transient read failure cannot wipe a user's rating.
- **Trigger scan flag** — set per user to request an on-demand scan without touching the cron schedule

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
- Library scoped by Navidrome `libraryId`; empty ID searches across all libraries

---

## Admin-level controls

| Setting | Default | Description |
|---------|---------|-------------|
| `sync_schedule` | `0 */6 * * *` | Cron expression for recurring sync |
| `user_scan_cooldown_hours` | `24` | Minimum hours between user-triggered scans |
| `max_songs_per_run` | `500` | Song cap per scheduled run (0 = unlimited) |
| `incremental_sync` | `true` | Skip files whose mtime predates the last successful scan; set false to force a full rescan every run |
| `dry_run` | `false` | Run the full scan pipeline without writing any ratings; logs `[DRY RUN] would_rate / would_clear` instead of calling `setRating`; does not advance the incremental-sync threshold |

---

## Subsonic API usage

- Paginates `search3` (500 songs/page) to fetch the full song list per user/library
- Calls `setRating` once per song where a tag was found
- Reads `userRating` from the search response to implement skip-already-rated

---

## What the plugin does NOT do

- Does not write tags back to files (read-only access to the filesystem)
- Does not support container formats beyond MP3, FLAC, Ogg-Vorbis, Opus, WAV, DSF, M4A/AAC, and WMA (AIFF, WavPack, DFF, etc. are skipped with a warning)
- Does not support tag formats beyond WMP POPM, iTunes POPM, MediaMonkey/foobar2000 FMPS_Rating, and foobar2000 RATING
- Does not import ratings in the other direction (Navidrome → file)
- Does not deduplicate the Subsonic `search3` request itself — incremental sync skips per-file work, but the song list is still fetched in full each run
