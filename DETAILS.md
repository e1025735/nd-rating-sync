# nd-rating-sync — Details

This file contains the plugin's rating value mappings, container/tag resolution behavior, and runtime workflow details.

## How the plugin reads files

Navidrome does not provide a direct sandbox filesystem path for songs. `nd-rating-sync` resolves `libraryId` into the host library mount point and reads files from the mounted directory tree inside the plugin sandbox.

The plugin builds a file index for each library mount keyed by exact file size and extension. A Navidrome song is matched only when there is exactly one candidate file. If no unique match is found, the song is skipped as unreadable and is never treated as "tag absent," so it cannot accidentally clear an existing rating.

The plugin also never trusts the `search3.path` value for the real filesystem location; it uses the host-mounted library tree instead.

## Incremental sync

After the first full pass, recurring scans become nearly instant: the
plugin records the scan-start timestamp in Navidrome's KV store, keyed per
`(library, user)`. On the next run, any file whose `mtime` predates that
timestamp is skipped without being read — and if Navidrome hasn't rescanned
the library at all, the **[change-detection gate](#change-detection-gate)**
skips even the song listing.

This is fast and correct because most music tag editors (MediaMonkey,
foobar2000, Mp3tag, Picard, Kid3, Strawberry…) update the file's
modification time the moment they save a tag change. So if a file's mtime
hasn't moved, neither has its rating tag.

**When should I disable it?**
- Right after you change a user's `ratingTagOrder` — the new order won't
  re-evaluate unchanged files until they're touched again. Set
  `incremental_sync = false` for one run to force a full pass.
- For debugging, when you want to see every song processed.

## Change-detection gate

Incremental sync skips the per-*file* work, but a plain sweep would still ask
Navidrome to enumerate every song (`search3`, paginated) on every run. The
change-detection gate removes that cost too. Before scanning a
`(library, user)` pair, the plugin reads the library's last-scan time from
Navidrome (one lightweight metadata call). If Navidrome has **not** rescanned
the library since the plugin's last successful sync, the entire pair is
skipped — no song listing at all.

The upshot: a fully-synced, unchanged library costs about one metadata call
per run no matter how large it is. That's what makes a frequent
`sync_schedule` (hourly, or even every 15 minutes) cheap.

Notes:
- The gate is part of incremental sync — it's active only when
  `incremental_sync = true`.
- It aligns the plugin to Navidrome's own scans. If you edit a tag on a file
  that's **already** in the library, the new rating is applied after
  Navidrome's next library scan picks up the change. To apply it immediately,
  run one pass with `incremental_sync = false`.
- It never *loses* work: any uncertainty (e.g. a library Navidrome reports as
  never-scanned) falls back to a normal full listing.

State is stored under the KV key `last-synced:{libraryId}:{username}` and
survives plugin reloads. KV-store failures are non-fatal: a missing or
malformed value falls back to a full scan; a failed write means the next
run does redundant work, never incorrect work.

## Rating value mapping

### FMPS_Rating

FMPS_Rating is written as a float between `0.0` and `1.0`.

| Tag value | Stars |
|-----------|-------|
| `0`, `0.0`, empty, missing | (unrated → skip) |
| `0.01` – `0.20` | ★ |
| `0.21` – `0.40` | ★★ |
| `0.41` – `0.60` | ★★★ |
| `0.61` – `0.80` | ★★★★ |
| `0.81` – `1.00` | ★★★★★ |

### foobar2000 RATING

The foobar2000 `RATING` tag is a plain integer. Anything outside 1–5 is treated as unrated.

| Tag value | Stars |
|-----------|-------|
| `0`, empty, `6+`, non-numeric | (unrated → skip) |
| `1` – `5` | ★ – ★★★★★ |

### WMP POPM

WMP ratings use non-linear fixed points. This includes WMP-style ratings also used by MusicBee. The plugin maps the tag byte to stars as follows:

| POPM byte | Stars |
|-----------|-------|
| `0` | (unrated → skip) |
| `1` | ★ |
| `2` – `64` | ★★ |
| `65` – `128` | ★★★ |
| `129` – `196` | ★★★★ |
| `197` – `255` | ★★★★★ |

### iTunes POPM

iTunes ratings use a linear scale in steps of 20 across 0–100.

| POPM byte | Stars |
|-----------|-------|
| `0` | (unrated → skip) |
| `1` – `20` | ★ |
| `21` – `40` | ★★ |
| `41` – `60` | ★★★ |
| `61` – `80` | ★★★★ |
| `81` – `255` | ★★★★★ |

### MusicBee

MusicBee uses a simple 0–100 scale in steps of 20 for tags written in the
freeform `RATING` style used by Vorbis/MP4 containers. For POPM / WMA-style
ratings, the plugin uses the same WMP-style non-linear breakpoints.

| Tag value | Stars |
|-----------|-------|
| `0`, empty, missing | (unrated → skip) |
| `20` | ★ |
| `40` | ★★ |
| `60` | ★★★ |
| `80` | ★★★★ |
| `100` | ★★★★★ |

## How the plugin works

The plugin is executed inside the Navidrome sandbox and is subject to a hard 30-second callback timeout. It therefore works in short budgeted chunks and reschedules continuations until the scan completes.

```
Navidrome startup
      │
      ▼
plugin OnInit
      │  schedules recurring sync (cron)
      │  schedules one-shot immediate sync
      │
      └──── recurring / immediate sync ─────────────────────────┐
                                                                │
                                                                ▼
                                              process one time-budgeted chunk
                                                          │
                              ┌─ change-detection gate (incremental, full sweep)  ──┐
                              │  Navidrome rescanned this library since last sync?  │
                              │     no → skip whole (library,user) pair, no listing │
                              └───────────────────────────────────────┬─────────────┘
                                                                      │ yes
                       ┌────────────────────────────────┬─────────────┴───────────────────┬───────────────────────────┐
                       │                                │                                 │                           │
                       ▼                                ▼                                 ▼                           │
            load last-synced threshold     SubsonicAPICall("search3") →     LibraryGetLibrary(id) → mount point       │
            from KVStore (incremental)     paginate songs (id, size, …)     walk mount, index files by (size, ext)    │
                      │                                 │                                 │                           │
                      └─────────────────────────── for each song ─────────────────────────┴───────────────────────────┘
                                       │
                                       ├─ song already rated in Navidrome?  → skip (skip_already_rated)
                                       │
                                       ├─ match song → file by exact (size, suffix) → no unique match? skip (unreadable, never clears)
                                       │
                                       ├─ matched file mtime < threshold?          → skip (unchanged)
                                       │
                                       ├─ read file (≤ 64 MiB)     → fail or oversize? skip (unreadable, never clears)
                                       │
                                       ├─ parse tags (ID3v2 / Vorbis / MP4 / ASF)
                                       │
                                       ├─ pick the first match by ratingTagOrder
                                       │
                                       ├─ tag found?  → SubsonicAPICall("setRating?id=…&rating=N")
                                       │
                                       └─ no tag found + clear_rating_if_untagged?
                                                       → SubsonicAPICall("setRating?id=…&rating=0")
                                                              │
                                                              ▼
                                                budget (~20s) reached with songs still pending?
                                                  → reschedule a one-time continuation carrying the cursor, return
                                                (library, user) fully processed?
                                                  → save scan-start timestamp to KVStore (incremental_sync)
```
