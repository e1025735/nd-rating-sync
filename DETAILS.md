# nd-rating-sync — Details

This file contains the plugin's rating value mappings, container/tag resolution behavior, and runtime workflow details.

## How the plugin reads files

Navidrome does not provide a direct sandbox filesystem path for songs. `nd-rating-sync` resolves `libraryId` into the host library mount point and reads files from the mounted directory tree inside the plugin sandbox.

For each page of songs the plugin asks Navidrome's matcher service (Navidrome 0.63.0+) to look the songs up by ID. With filesystem access granted, each result carries the path Navidrome stored for the file, relative to the library root; the plugin opens that path inside the mount. A song the matcher returns no track for, whose file Navidrome marks as missing, whose path lies outside the library, or whose file is not in the mount is skipped as unreadable and is never treated as "tag absent," so it cannot accidentally clear an existing rating.

The same rule applies inside the file: a file counts as "no tag" only when its container was read to a clean end and holds no rating tag. A file that is not what its extension says, is cut off, or has a size field that does not fit the file is unreadable, never untagged.

The `path` that the Subsonic `search3` API reports is a made-up string, so the plugin never uses it.

## No rating, 0 stars and half stars

A file the plugin read to a clean end that holds no rating for any source in
the user's `ratingTagOrder` is **rated 0 stars**. The same holds for a rating
field that is empty or `0` (POPM byte `0`, `FMPS_Rating` `0.0`, `RATING=0`,
`WM/SharedUserRating` `0`). With `clear_rating_if_untagged` on (per user, or
through `default_clear_rating_if_untagged`), the plugin then sets the song's
Navidrome rating to 0 as well. Songs skipped by `skip_already_rated` are not
read, so to clear ratings that already exist in Navidrome, set
`skip_already_rated` to No too.

Two kinds of files are **never** set to 0:

- **Unreadable files** (see above): the plugin does not know what they hold.
- **Files rated with a value Navidrome cannot show**: a half star (MusicBee or
  MediaMonkey POPM byte `13`/`54`/`118`/`186`/`242`, `FMPS_Rating` `0.7`,
  `RATING=70`) or a value outside the source's scale (foobar2000 `RATING=7`,
  `WM/SharedUserRating` `150`). The file is rated, so its Navidrome rating is
  left as it is. The chunk summary counts such songs as
  `skipped_not_whole_star`. A whole-star rating from a later source in
  `ratingTagOrder` still wins.

Only the sources in `ratingTagOrder` count: a rating written by a player you
did not list is not read, so such a file counts as having no rating.

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
hasn't moved, neither has its rating tag. The one exception is a song
Navidrome added since the last run (for example a renamed or moved file,
which keeps its old mtime but is imported as a new, unrated song): it is
always read.

Changing a user's `ratingTagOrder`, `skip_already_rated` or
`clear_rating_if_untagged` is detected automatically: the stored timestamp
carries a fingerprint of those settings, so that user's next sync reads every
file once and applies the new settings to unchanged files too.

**When should I disable it?**
- For debugging, when you want to see every song processed on every run.

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
survives plugin reloads and unrelated config changes. KV-store failures are non-fatal: a missing or
malformed value falls back to a full scan; a failed write means the next
run does redundant work, never incorrect work.

## Rating value mapping

### FMPS_Rating

FMPS_Rating is the freedesktop rating field (Strawberry, Clementine, Amarok and
others). The `FMPS` key reads it in every container; `MediaMonkey` also reads it
as a fallback. It is written as a float between `0.0` and `1.0`. Only whole stars
are accepted; half stars (`0.1`, `0.3`, … `0.9`) are left alone because
Navidrome has no half stars. A value that is not a number or lies outside
`0.0`–`1.0` (e.g. `4` or `80` written by a tool that confused the scale) makes the
file unreadable: nothing is written or cleared, and the warning names the value.
A valid rating from a source earlier in `ratingTagOrder` still wins.

| Tag value | Stars |
|-----------|-------|
| `0`, `0.0`, empty, missing | no rating (0 stars) |
| half stars | (not a whole star → left alone, never cleared) |
| `0.2` | ★ |
| `0.4` | ★★ |
| `0.6` | ★★★ |
| `0.8` | ★★★★ |
| `1.0` | ★★★★★ |
| not a number, below `0` or above `1.0` | (file unreadable → skip, never cleared) |

### MediaMonkey and MusicBee POPM

MediaMonkey (`POPM` email `no@email`) and MusicBee (`POPM` email `MusicBee`)
share one byte table, which also has half stars (½★ = `13`, 1½★ = `54`,
2½★ = `118`, 3½★ = `186`, 4½★ = `242`). Because ½★ is a larger byte than 1★,
these frames are decoded by exact value instead of by range:

| POPM byte | Stars |
|-----------|-------|
| `1` (MediaMonkey 3.x: `23`) | ★ |
| `64` | ★★ |
| `128` | ★★★ |
| `196` | ★★★★ |
| `255` (MediaMonkey 3.x: `252`) | ★★★★★ |
| `0` | no rating (0 stars) |
| anything else, incl. half stars | (not a whole star → left alone, never cleared) |

MediaMonkey writes its rating as `POPM` in MP3, as `RATING` (0–100) in
FLAC/Ogg/Opus and as `rate` in M4A. These take precedence over an
`FMPS_Rating` tag in the same file when `MediaMonkey` is in `ratingTagOrder`.

### foobar2000 RATING

The foobar2000 `RATING` tag is a plain integer. `0` or empty means no rating; anything else outside 1–5 is left alone. In MP3 files foobar2000 writes its rating as a POPM frame with the email `Windows Media Player 9 Series`; when there is no `RATING` TXXX frame, the `foobar2000` key reads that frame with the Windows Media Player scale.

| Tag value | Stars |
|-----------|-------|
| `0`, empty | no rating (0 stars) |
| `6+`, non-numeric | (out of range → left alone, never cleared) |
| `1` – `5` | ★ – ★★★★★ |

### WMP POPM

WMP ratings use non-linear fixed points. The plugin maps the tag byte to stars
by range (MusicBee and MediaMonkey POPM frames use the exact-value table
above instead):

| POPM byte | Stars |
|-----------|-------|
| `0` | (unrated → skip) |
| `1` | ★ |
| `2` – `64` | ★★ |
| `65` – `128` | ★★★ |
| `129` – `196` | ★★★★ |
| `197` – `255` | ★★★★★ |

### WMA `WM/SharedUserRating`

In WMA files Windows Media Player stores the rating as a DWORD on a 0–99
scale (it writes `1`, `25`, `50`, `75`, `99`). MusicBee's WMA ratings use the
same field. The plugin groups the values like the Windows `System.Rating`
property:

| Value | Stars |
|-------|-------|
| `0` | (unrated → skip) |
| `1` – `12` | ★ |
| `13` – `37` | ★★ |
| `38` – `62` | ★★★ |
| `63` – `87` | ★★★★ |
| `88` – `99` | ★★★★★ |
| `100`+ | (out of range → left alone, never cleared) |

### iTunes POPM

iTunes ratings use a linear scale in steps of 20 across 0–100. These are iTunes-style tags written by other tools: iTunes and Apple Music keep their ratings in the library database and do not write them to the file.

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
freeform `RATING` style used by Vorbis containers and the `RATE` freeform atom
it writes in MP4/M4A files (MediaMonkey writes the same scale in the same
fields). Half-star values (`10`, `30`, … `90`) and values above `100` are
left alone because Navidrome has no half stars. MusicBee's POPM frame uses the
exact-value table under *MediaMonkey and MusicBee POPM*, and its WMA rating
uses `WM/SharedUserRating` (see above).

| Tag value | Stars |
|-----------|-------|
| `0`, empty, missing | no rating (0 stars) |
| `10`, `30`, … `90`, above `100`, non-numeric | (not a whole star → left alone, never cleared) |
| `20` | ★ |
| `40` | ★★ |
| `60` | ★★★ |
| `80` | ★★★★ |
| `100` | ★★★★★ |

## How the plugin works

The plugin is executed inside the Navidrome sandbox and is subject to a hard 30-second callback timeout. It therefore works in short budgeted chunks (15 seconds each) and reschedules continuations until the scan completes.

If a user's song listing fails (for example a username Navidrome does not
know) or the matcher cannot look up a page of songs, the plugin waits
30 seconds and tries again. After three failed attempts it skips that
`(library, user)` pair until the next scheduled sync and logs a warning, so a
lasting problem can never keep the plugin busy forever.

A file whose tag cannot be read or parsed — cut off, corrupt, larger than
16 MiB, or an unsupported ID3v2.2 tag — is counted as *unreadable*, never as
*no tag*, so `clear_rating_if_untagged` cannot clear its rating.

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
            from KVStore (incremental)     paginate songs (id, rating, …)   MatcherMatchSongs(page IDs) → paths       │
                      │                                 │                                 │                           │
                      └─────────────────────────── for each song ─────────────────────────┴───────────────────────────┘
                                       │
                                       ├─ song already rated in Navidrome?  → skip (skip_already_rated)
                                       │
                                       ├─ song → file via the matcher's path in the mount → not found? skip (unreadable, never clears)
                                       │
                                       ├─ mtime < threshold and song not newly added? → skip (unchanged)
                                       │
                                       ├─ read only the tag area (≤ 16 MiB) → fail or oversize? skip (unreadable, never clears)
                                       │
                                       ├─ parse tags (ID3v2 / Vorbis / MP4 / ASF) → corrupt? skip (unreadable, never clears)
                                       │
                                       ├─ pick the first match by ratingTagOrder
                                       │
                                       ├─ tag found and different from Navidrome's rating?  → SubsonicAPICall("setRating?id=…&rating=N")
                                       │
                                       ├─ half star or out-of-range value? → skip (rated, never cleared)
                                       │
                                       └─ no rating (0 stars) + clear_rating_if_untagged + Navidrome has a rating?
                                                       → SubsonicAPICall("setRating?id=…&rating=0")
                                                              │
                                                              ▼
                                                budget (15 s) reached with songs still pending?
                                                  → reschedule a one-time continuation carrying the cursor, return
                                                listing or matcher lookup failed?
                                                  → retry in 30 s; after 3 attempts skip the pair until the next sync
                                                (library, user) fully processed?
                                                  → save scan-start timestamp to KVStore (incremental_sync)
```
