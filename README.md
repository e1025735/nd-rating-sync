# nd-rating-sync

A [Navidrome](https://www.navidrome.org/) plugin that reads embedded star-rating
tags from your music files and syncs them into Navidrome's own user-rating
system via the internal Subsonic API.

## Why this plugin exists

Navidrome reads many tag fields during a library scan, but it does **not**
automatically convert your existing rating tags (FMPS_Rating, POPM, plain
RATING…) into the clickable ★★★★★ user rating that appears in the UI and
drives smart-playlist filtering. If you've spent years rating tracks in
MediaMonkey, foobar2000, iTunes, or Windows Media Player, those ratings live
inside the file itself but are invisible to a fresh Navidrome install.

This plugin closes that gap. On a schedule (or on demand), it walks your
library, reads the embedded rating tag from each file, and calls
`setRating` so the value shows up in Navidrome exactly as if you had
clicked the stars yourself.

The plugin is **read-only on the filesystem**. It never writes back to your
files.

---

## Supported file formats

| Container | Tag system | Extensions |
|-----------|------------|------------|
| MP3 | ID3v2 (TXXX, POPM) | `.mp3` |
| FLAC | Vorbis comments | `.flac` |
| Ogg-Vorbis | Vorbis comments | `.ogg`, `.oga` |
| Opus | Vorbis comments (`OpusTags`) | `.opus` |
| WAV | ID3v2 chunk inside RIFF | `.wav` |
| DSD Stream File | ID3v2 block (offset from DSD header) | `.dsf` |
| M4A / AAC | MP4 freeform atoms (`----`) | `.m4a`, `.aac`, `.mp4` |
| WMA | ASF Extended Content Description | `.wma` |

Files with other extensions (WavPack, AIFF, DFF, …) are skipped with a
warning in the log.

## Supported rating tags

`ratingTagOrder` values are *source applications*. Each source maps to
whatever tag(s) that application typically writes, in whichever container
you happen to use. Sources that have no representation in a given container
(e.g. WMP doesn't write FLAC) simply never match — listing them in the
order is harmless.

| Source | MP3 / WAV / DSF (ID3v2) | FLAC / Ogg / Opus (Vorbis) | M4A / AAC (MP4 atom) | WMA (ASF) | Value scale |
|--------|-------------------------|----------------------------|-----------------------|-----------|-------------|
| `MediaMonkey` | TXXX `FMPS_Rating` | `FMPS_RATING` | `FMPS_Rating` freeform | `FMPS_Rating` Unicode | Float 0.0–1.0 |
| `foobar2000` | TXXX `RATING` | `RATING` | `RATING` freeform (uppercase) | — | Integer 1–5 |
| `WMP` | POPM (`Windows Media Player 9 Series`) | — | — | `WM/SharedUserRating` | Byte 0–255, WMP scale |
| `iTunes` | POPM (`iTunes` / `com.apple.iTunes`) | — | `rating` freeform (lowercase) | — | Byte / int 0–100, iTunes scale |

### Rating value mapping

**FMPS_Rating** — written as a float between `0.0` and `1.0`:

| Tag value | Stars |
|-----------|-------|
| `0`, `0.0`, empty, missing | (unrated → skip) |
| `0.01` – `0.20` | ★ |
| `0.21` – `0.40` | ★★ |
| `0.41` – `0.60` | ★★★ |
| `0.61` – `0.80` | ★★★★ |
| `0.81` – `1.00` | ★★★★★ |

**foobar2000 RATING** — plain integer, anything outside 1–5 is treated as unrated:

| Tag value | Stars |
|-----------|-------|
| `0`, empty, `6+`, non-numeric | (unrated → skip) |
| `1` – `5` | ★ – ★★★★★ |

**WMP POPM** — non-linear: WMP itself writes one of five fixed bytes
(`1`, `25`, `50`, `75`, `99`); the ranges between those points all collapse
to the lower star:

| POPM byte | Stars |
|-----------|-------|
| `0` | (unrated → skip) |
| `1` – `24` | ★ |
| `25` – `49` | ★★ |
| `50` – `74` | ★★★ |
| `75` – `98` | ★★★★ |
| `99` – `255` | ★★★★★ |

**iTunes POPM** — linear in steps of 20 across 0–100:

| POPM byte | Stars |
|-----------|-------|
| `0` | (unrated → skip) |
| `1` – `20` | ★ |
| `21` – `40` | ★★ |
| `41` – `60` | ★★★ |
| `61` – `80` | ★★★★ |
| `81` – `255` | ★★★★★ |

---

## Configuration

The plugin uses a **hierarchical, per-library / per-user configuration**.
A single Navidrome instance can host multiple libraries, each scoped to a
different set of users, and each user can have their own preferred order of
rating tag sources.

The full schema lives in [manifest.json](manifest.json) — but the easiest
way to understand the config is by reading the annotated example:

→ **[config.example.json](config.example.json)**

That file shows two users (Alice and Bob) configured side-by-side in the
same library with different rating preferences. Below is a walkthrough of
the same example. You enter these settings via the Navidrome UI:
**avatar → Plugins → nd-rating-sync → Settings**.

### Top-level admin scalars

| Key | Default | Meaning |
|-----|---------|---------|
| `sync_schedule` | `0 * * * *` | Cron expression for the recurring background scan. The default runs **hourly**. A run whose libraries Navidrome hasn't rescanned since the last sync is skipped cheaply (one metadata call per library, no song listing), so a frequent schedule is inexpensive — use `*/15 * * * *` for near-real-time pickup. See **[Change-detection gate](#change-detection-gate)**. |
| `user_scan_cooldown_hours` | `24` | Minimum hours between two manual user-triggered scans for the same user. Set to `0` to disable the cooldown. |
| `incremental_sync` | `true` | When enabled, recurring scans skip files whose mtime hasn't changed since the previous successful run. See **[Incremental sync](#incremental-sync)** below. |
| `dry_run` | `false` | When true, the full scan pipeline runs but no `setRating` calls are made. Every rating that would be written or cleared is logged with a `[DRY RUN]` prefix. The incremental-sync threshold is not updated. Use this to verify tag parsing before the first real import. |
| `default_trigger_user_scan` | `null` | Admin-level default for `trigger_user_scan`. Applied to any user whose per-user setting is `null`. `null` here means no admin default — the plugin falls back to `false`. |
| `default_skip_already_rated` | `null` | Admin-level default for `skip_already_rated`. Applied to any user whose per-user setting is `null`. `null` here means no admin default — the plugin falls back to `true`. |
| `default_clear_rating_if_untagged` | `null` | Admin-level default for `clear_rating_if_untagged`. Applied to any user whose per-user setting is `null`. `null` here means no admin default — the plugin falls back to `false`. |

### Per-library settings

For every Navidrome library you want synced, add an entry to `libraries[]`:

| Field | Required | Meaning |
|-------|----------|---------|
| `libraryId` | yes | The internal ID of the library, visible on Navidrome's *Libraries* admin page. **Not** the display name. |
| `libraryName` | no | Human-readable label. Purely for your own reference — the plugin ignores it. |
| `users` | yes | Per-user settings within this library. See below. |

A user can appear in multiple libraries with different settings; they don't
have to be in all of them. In `config.example.json`, Alice is in both
libraries while Bob is only in the main one.

### Per-user settings

| Field | Default | Meaning |
|-------|---------|---------|
| `username` | (required) | Must exactly match the Navidrome username. The same user must also be listed under the plugin's *User Access* permissions panel — Navidrome enforces this independently. |
| `trigger_user_scan` | `null` | Accepts `true`, `false`, or `null`. Set to `true` to enable on-demand rating scans for this user. The plugin checks every 15 minutes and runs a scan once the cooldown has elapsed; the flag is **not** reset automatically, so leaving it `true` causes the scan to repeat each cooldown window. Set it back to `false` (or `null`) when you no longer want the scans. Subject to `user_scan_cooldown_hours`. `null` = inherit from `default_trigger_user_scan`, then plugin default (`false`). |
| `skip_already_rated` | `null` | Accepts `true`, `false`, or `null`. When `true`, songs that already have a Navidrome user rating are left untouched. Set `false` to let file tags overwrite existing Navidrome ratings — useful for one-off bulk imports. `null` = inherit from `default_skip_already_rated`, then plugin default (`true`). |
| `clear_rating_if_untagged` | `null` | Accepts `true`, `false`, or `null`. When `true`, songs whose audio file contains no recognised rating tag will have their Navidrome rating set to 0 (removed). Requires `skip_already_rated: false` to also clear ratings on songs that were previously rated in Navidrome — otherwise already-rated songs are skipped before the file is read and their ratings will not be cleared. `null` = inherit from `default_clear_rating_if_untagged`, then plugin default (`false`). |
| `ratingTagOrder` | `["WMP", "iTunes", "MediaMonkey", "foobar2000"]` | Ordered list of source applications to try. The first match found in a given file wins. Trim or reorder the list to match the workflow you actually use; sources you don't use can stay in the list (they just never match) or be removed for clarity. |

### Two users, two workflows

The example file demonstrates how `ratingTagOrder` lets each user mirror the
tagging workflow they actually use:

- **Alice** rates her library in MediaMonkey/Strawberry, which writes
  `FMPS_Rating`. Her order is `["MediaMonkey", "foobar2000"]` — the first
  tag found in each file wins. She also keeps `skip_already_rated: true`,
  so any rating she's already given a track inside the Navidrome web UI
  takes priority over the file tag.

- **Bob** has decades of foobar2000 ratings (plain `RATING` integer 1–5),
  with some older albums tagged by WMP and iTunes. His order is
  `["foobar2000", "WMP", "iTunes"]`. He also sets `skip_already_rated:
  false` to let the file tags fully overwrite whatever Navidrome shows.

You can configure **the same** Navidrome user with **different orders in
different libraries** if your tagging conventions vary across collections.

---

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
  `incremental_sync = true`, and is bypassed by manual user-triggered scans
  (so an explicit "scan now" always re-reads).
- It aligns the plugin to Navidrome's own scans. If you edit a tag on a file
  that's **already** in the library, the new rating is applied after
  Navidrome's next library scan picks up the change. To apply it immediately,
  trigger a user scan (`trigger_user_scan`) or run one pass with
  `incremental_sync = false`.
- It never *loses* work: any uncertainty (e.g. a library Navidrome reports as
  never-scanned) falls back to a normal full listing.

State is stored under the KV key `last-synced:{libraryId}:{username}` and
survives plugin reloads. KV-store failures are non-fatal: a missing or
malformed value falls back to a full scan; a failed write means the next
run does redundant work, never incorrect work.

---

## How it works

```
Navidrome startup
      │
      ▼
plugin OnInit
      │  schedules recurring sync (cron)
      │  schedules one-shot immediate sync
      │  schedules trigger-check (every 15 min)
      │
      ├──── recurring / immediate sync ─────────────────────────┐
      │                                                          │
      └──── trigger-check (every 15 min) ──────────────┐         │
                                                       ▼         │
                              for each user with trigger_user_scan = true,
                              if cooldown elapsed: enqueue a one-time
                              single-user continuation (returns immediately)
                                                                 │
                                                                 ▼
                                              process one time-budgeted chunk
                                                          │
                              ┌─ change-detection gate (incremental, full sweep) ──┐
                              │  Navidrome rescanned this library since last sync?  │
                              │     no → skip whole (library,user) pair, no listing │
                              └────────────────────────┬────────────────────────────┘
                                                       │ yes
                       ┌───────────────────────────────┴──────────────────────┐
                       │                                                       │
                       ▼                                                       ▼
        load last-synced threshold              SubsonicAPICall("search3") → paginate
        from KVStore (incremental_sync)         every song accessible by user
                       │                                                      │
                       └──────────── for each song ──────────────────────────┘
                                       │
                                       ├─ song already rated in Navidrome?  → skip (skip_already_rated)
                                       │
                                       ├─ os.Stat(path).mtime < threshold?  → skip (unchanged)
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

### Staying within Navidrome's 30s call limit

Navidrome force-closes any plugin call that runs longer than **30 seconds**
(a hard, non-configurable host limit). A first-time scan of a large library
would blow past that, so each callback works for a fixed **~20s budget** and
then stops. If songs remain, it serialises its position — `(library, user,
song offset)` — into the payload of a fresh one-time callback that the host
runs again immediately, resuming exactly where it left off. A big library is
therefore imported across a short chain of quick callbacks rather than one
long call, and you'll see log lines like `time budget reached – rescheduled
continuation …` ending in `sync complete`. Each callback runs in a fresh
WASM instance, so this progress lives entirely in the scheduler payload and
the KV store, never in memory. `setRating` is idempotent, so the rare
re-processed song on resume is harmless.

Because each continuation is rescheduled with **zero delay**, the chain runs
back-to-back and a large first import finishes in minutes. The
`sync_schedule` cron only decides how often a *fresh* sweep begins — not how
fast one finishes. A lightweight in-progress guard keeps a freshly-triggered
sweep from piling on top of one that's still running, so a frequent schedule
is safe even mid-import.

---

## Prerequisites

- Navidrome ≥ 0.60.0 (plugin system required)
- [TinyGo](https://tinygo.org/getting-started/install/) for building
- `zip` utility for packaging

## Build

```sh
git clone https://github.com/yourusername/nd-rating-sync
cd nd-rating-sync

# Point go.mod at your local Navidrome PDK if needed (see comments in go.mod),
# or use a published pseudo-version once available.

make
# Produces: nd-rating-sync.ndp
```

## Installation

1. Copy `nd-rating-sync.ndp` to your Navidrome plugins folder (default:
   `<DataFolder>/plugins`).

2. Enable plugins in your Navidrome config:

   ```toml
   [Plugins]
   Enabled    = true
   AutoReload = true   # convenient during development
   LogLevel   = "info"
   ```

3. Open the Navidrome web UI → click your avatar → **Plugins** → **Rescan**.

4. Enable **nd-rating-sync** and click the row to open its settings.

5. Fill in the configuration. Use [config.example.json](config.example.json)
   as a template — the form fields in the UI map one-to-one onto the
   structure shown there.

6. Under the plugin's **User Access** tab, add every Navidrome user you've
   listed inside any library's `users[]` (Alice and Bob in the example).
   This is a separate Navidrome-level permission and **must** be set, or
   `setRating` calls will fail with API error 50.

7. Under **Library Access**, grant access to the libraries whose IDs you
   used in `libraries[].libraryId`.

The plugin runs an immediate scan on activation, then repeats on the
`sync_schedule` cron. After the first pass, recurring scans are nearly
instant thanks to incremental sync — only files whose tags actually
changed are re-read.

---

## Troubleshooting

Set `[Plugins] LogLevel = "debug"` in Navidrome's config and grep the
Navidrome log for `nd-rating-sync:`. Each per-user run logs a summary line
like:

Normal run:
```
nd-rating-sync: done user="alice" – rated=12 cleared=3 skipped_already_rated=438 skipped_unchanged=2810 skipped_no_tag=15 skipped_unreadable=0 errors=0
```

Dry run (`dry_run=true`):
```
nd-rating-sync: [DRY RUN] done user="alice" – would_rate=12 would_clear=3 skipped_already_rated=438 skipped_unchanged=2810 skipped_no_tag=15 skipped_unreadable=0
```

The counters tell you what happened:

- **`rated`** — songs whose rating was just written to Navidrome.
- **`cleared`** — songs whose Navidrome rating was set to 0 because no tag
  was found (only appears when `clear_rating_if_untagged = true`).
- **`skipped_already_rated`** — songs that already had a Navidrome rating
  for this user (only meaningful when `skip_already_rated = true`).
- **`skipped_unchanged`** — songs whose file mtime predates the
  incremental-sync threshold. Should dominate after the first full pass.
- **`skipped_no_tag`** — songs that had no recognised rating tag (or whose
  tag was empty / unrated). Only counted when `clear_rating_if_untagged = false`.
- **`skipped_unreadable`** — songs the plugin could not open or whose
  container could not be parsed (I/O error, permission denied, unsupported
  extension). These are *never* cleared, even with
  `clear_rating_if_untagged = true`, because a transient read failure must
  not wipe the user's existing rating. Each occurrence is also logged as a
  warning with the underlying error.
- **`would_rate`** / **`would_clear`** — dry-run equivalents of `rated` / `cleared`; only appear when `dry_run = true`.
- **`errors`** — `setRating` failures or other per-song errors. Not present in dry-run output.

### Common issues

| Symptom | Likely cause |
|---------|-------------|
| `no libraries configured` | The `libraries` array is empty or missing. Add at least one library with at least one user. |
| `setRating` API error 50 (`user not authorised`) | The user is configured in `libraries[].users[]` but **not** added to the plugin's *User Access* tab in the Navidrome UI. Both are required. |
| `cannot open "/path/to/song.mp3": permission denied` | The plugin lacks *Library Access* for the library containing this song. |
| `skipping "/path/to/song.flac" – size NNN exceeds cap …` | The file is larger than the 64 MiB read cap (typically a misreported path or a sample-rate-extreme archival rip). Counted under `skipped_unreadable`; the existing Navidrome rating is left untouched. |
| `skipping … – supported formats are MP3, FLAC, Ogg, Opus, WAV, DSF, M4A/AAC and WMA (got .aiff)` | The file is in a container the plugin does not support. The song is silently passed over. |
| Ratings not updating after I edited a tag | Two layers gate this. (1) The change-detection gate waits for Navidrome to rescan the library — until then an already-indexed file isn't revisited. (2) Incremental sync then only re-reads files whose mtime moved (confirm with `ls -l`). To apply an edit immediately, set `trigger_user_scan = true` for the user (bypasses the gate) or run one pass with `incremental_sync = false`. |
| Log says `skipping … – library unchanged since last sync` | Normal and expected — the change-detection gate short-circuited a run because Navidrome hasn't rescanned that library since the last sync. This is what keeps frequent schedules cheap. |
| First run took forever, second run was fast | Working as designed — that's the whole point of incremental sync and the change-detection gate. |
| `KVStoreGet … failed – falling back to full scan` (warning) | The Navidrome KV store is unreachable for some reason. The current scan still works (as a full scan); investigate the underlying error. |
