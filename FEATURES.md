# nd-rating-sync — Feature List

## Core purpose
Reads embedded star-rating tags from MP3, FLAC, Ogg-Vorbis, and Opus files
and writes them to Navidrome's user-rating system via the Subsonic
`setRating` API. Navidrome does not import embedded ratings on its own;
this plugin bridges file tags and the UI.

---

## Sync triggers

| Trigger | Details |
|---------|---------|
| Immediate on load | One-shot scan runs as soon as the plugin initialises |
| Recurring (cron) | Configurable cron expression, default every 6 hours |
| User-triggered | `trigger_user_scan=true` flag checked every 15 min; one-shot scan per user |
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

## Supported tag formats

`ratingTagOrder` values are *source applications*, not container-specific keys. Each source maps to whatever tag(s) that application writes in each container. Sources without a representation in a given container (e.g. `WMP` for FLAC) are silently skipped — they just never match.

| Key | MP3 (ID3v2) | FLAC / Ogg / Opus (Vorbis comments) | Scale / notes |
|-----|-------------|-------------------------------------|---------------|
| `WMP` | POPM (email contains "windows media player") | — | Non-linear fixed points: 1/25/50/75/99 → 1–5 stars |
| `iTunes` | POPM (email contains "itunes" / "com.apple.itunes") | — | Linear 0–100 in steps of 20 → 1–5 stars |
| `MediaMonkey` | TXXX description "FMPS_Rating" | `FMPS_RATING` | Float 0.0–1.0, ceiling × 5 → 1–5 stars |
| `foobar2000` | TXXX description "RATING" | `RATING` | Integer 1–5 (0 / empty / out-of-range = unrated) |

---

## Per-user configuration

- **Tag priority order** — `ratingTagOrder` list; first tag *found in the file* wins
- **Skip already-rated** — songs with an existing Navidrome rating are left untouched (default on; can be disabled to allow overwrites)
- **Clear rating if untagged** — when enabled, songs whose file contains no recognised rating tag have their Navidrome rating removed (set to 0); requires `skip_already_rated=false` to also affect previously-rated songs
- **Trigger scan flag** — set per user to request an on-demand scan without touching the cron schedule

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
- Does not support container formats beyond MP3, FLAC, Ogg-Vorbis, and Opus (AAC/M4A, WAV, etc. are skipped with a warning)
- Does not support tag formats beyond WMP POPM, iTunes POPM, MediaMonkey/foobar2000 FMPS_Rating, and foobar2000 RATING
- Does not import ratings in the other direction (Navidrome → file)
- Does not deduplicate the Subsonic `search3` request itself — incremental sync skips per-file work, but the song list is still fetched in full each run
