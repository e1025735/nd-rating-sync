# nd-rating-sync — Feature List

## Core purpose
Reads embedded star-rating tags from MP3 files and writes them to Navidrome's
user-rating system via the Subsonic `setRating` API. Navidrome does not import
embedded ratings on its own; this plugin bridges file tags and the UI.

---

## Sync triggers

| Trigger | Details |
|---------|---------|
| Immediate on load | One-shot scan runs as soon as the plugin initialises |
| Recurring (cron) | Configurable cron expression, default every 6 hours |
| User-triggered | `trigger_user_scan=true` flag checked every 15 min; one-shot scan per user |
| Cooldown guard | Configurable minimum gap between user-triggered scans (default 24 h) |

---

## Supported tag formats (MP3 / ID3v2 only)

| Key | ID3v2 frame | Scale / notes |
|-----|-------------|---------------|
| `WMP` | POPM (email contains "windows media player") | Non-linear fixed points: 1/25/50/75/99 → 1–5 stars |
| `iTunes` | POPM (email contains "itunes" / "com.apple.itunes") | Linear 0–100 in steps of 20 → 1–5 stars |
| `MediaMonkey` | TXXX description "FMPS_Rating" | Float 0.0–1.0, ceiling × 5 → 1–5 stars |

---

## Per-user configuration

- **Tag priority order** — `ratingTagOrder` list; first tag *found in the file* wins
- **Skip already-rated** — songs with an existing Navidrome rating are left untouched (default on; can be disabled to allow overwrites)
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

---

## Subsonic API usage

- Paginates `search3` (500 songs/page) to fetch the full song list per user/library
- Calls `setRating` once per song where a tag was found
- Reads `userRating` from the search response to implement skip-already-rated

---

## What the plugin does NOT do

- Does not write tags back to files (read-only access to the filesystem)
- Does not support file formats other than MP3 (FLAC, AAC, etc. are skipped with a warning)
- Does not support tag formats beyond WMP POPM, iTunes POPM, and MediaMonkey FMPS_Rating
- Does not import ratings in the other direction (Navidrome → file)
