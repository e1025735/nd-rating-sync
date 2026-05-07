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

Files with other extensions (AAC/M4A, WAV, WMA, …) are skipped with a
warning in the log.

## Supported rating tags

`ratingTagOrder` values are *source applications*. Each source maps to
whatever tag(s) that application typically writes, in whichever container
you happen to use. Sources that have no representation in a given container
(e.g. WMP doesn't write FLAC) simply never match — listing them in the
order is harmless.

| Source | MP3 (ID3v2) | FLAC / Ogg / Opus (Vorbis) | Value scale |
|--------|-------------|----------------------------|-------------|
| `MediaMonkey` | TXXX `FMPS_Rating` | `FMPS_RATING` | Float 0.0–1.0 |
| `foobar2000` | TXXX `RATING` | `RATING` | Integer 1–5 |
| `WMP` | POPM (`Windows Media Player 9 Series`) | — | Byte 0–255, WMP scale |
| `iTunes` | POPM (`iTunes` / `com.apple.iTunes`) | — | Byte 0–100, iTunes scale |

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
| `sync_schedule` | `0 */6 * * *` | Cron expression for the recurring background scan. The default runs every 6 hours. |
| `user_scan_cooldown_hours` | `24` | Minimum hours between two manual user-triggered scans for the same user. Set to `0` to disable the cooldown. |
| `max_songs_per_run` | `500` | Hard cap on how many songs a single scheduled run will process. Set to `0` for unlimited. The cap protects against accidental runaways on very large libraries. |
| `incremental_sync` | `true` | When enabled, recurring scans skip files whose mtime hasn't changed since the previous successful run. See **[Incremental sync](#incremental-sync)** below. |
| `dry_run` | `false` | When true, the full scan pipeline runs but no `setRating` calls are made. Every rating that would be written or cleared is logged with a `[DRY RUN]` prefix. The incremental-sync threshold is not updated. Use this to verify tag parsing before the first real import. |

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
| `trigger_user_scan` | `false` | Set to `true` to request an immediate one-shot rating scan for this user. The plugin checks every 15 minutes, runs the scan, then automatically resets the flag. Subject to `user_scan_cooldown_hours`. |
| `skip_already_rated` | `true` | When `true`, songs that already have a Navidrome user rating are left untouched. Set `false` to let file tags overwrite existing Navidrome ratings — useful for one-off bulk imports. |
| `clear_rating_if_untagged` | `false` | When `true`, songs whose audio file contains no recognised rating tag will have their Navidrome rating set to 0 (removed). Requires `skip_already_rated: false` to also clear ratings on songs that were previously rated in Navidrome — otherwise already-rated songs are skipped before the file is read and their ratings will not be cleared. |
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
timestamp is skipped without being read.

This is fast and correct because most music tag editors (MediaMonkey,
foobar2000, Mp3tag, Picard, Kid3, Strawberry…) update the file's
modification time the moment they save a tag change. So if a file's mtime
hasn't moved, neither has its rating tag.

**When should I disable it?**
- Right after you change a user's `ratingTagOrder` — the new order won't
  re-evaluate unchanged files until they're touched again. Set
  `incremental_sync = false` for one run to force a full pass.
- For debugging, when you want to see every song processed.

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
                              if cooldown elapsed: queue a scan
                                                                 │
                                                                 ▼
                                                    runSyncForUser(lib, user)
                                                          │
                       ┌──────────────────────────────────┴──────────────────┐
                       │                                                      │
                       ▼                                                      ▼
        load last-synced threshold              SubsonicAPICall("search3") → paginate
        from KVStore (incremental_sync)         every song accessible by user
                       │                                                      │
                       └──────────── for each song ──────────────────────────┘
                                       │
                                       ├─ song already rated in Navidrome?  → skip (skip_already_rated)
                                       │
                                       ├─ os.Stat(path).mtime < threshold?  → skip (unchanged)
                                       │
                                       ├─ os.ReadFile(path)
                                       │
                                       ├─ parse tags (ID3v2 / Vorbis comments)
                                       │
                                       ├─ pick the first match by ratingTagOrder
                                       │
                                       ├─ tag found?  → SubsonicAPICall("setRating?id=…&rating=N")
                                       │
                                       └─ no tag found + clear_rating_if_untagged?
                                                       → SubsonicAPICall("setRating?id=…&rating=0")
                       │
                       ▼
        save scan-start timestamp to KVStore (incremental_sync)
```

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
nd-rating-sync: done user="alice" – rated=12 cleared=3 skipped_already_rated=438 skipped_unchanged=2810 skipped_no_tag=15 errors=0
```

Dry run (`dry_run=true`):
```
nd-rating-sync: [DRY RUN] done user="alice" – would_rate=12 would_clear=3 skipped_already_rated=438 skipped_unchanged=2810 skipped_no_tag=15
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
- **`would_rate`** / **`would_clear`** — dry-run equivalents of `rated` / `cleared`; only appear when `dry_run = true`.
- **`errors`** — `setRating` failures or other per-song errors. Not present in dry-run output.

### Common issues

| Symptom | Likely cause |
|---------|-------------|
| `no libraries configured` | The `libraries` array is empty or missing. Add at least one library with at least one user. |
| `setRating` API error 50 (`user not authorised`) | The user is configured in `libraries[].users[]` but **not** added to the plugin's *User Access* tab in the Navidrome UI. Both are required. |
| `cannot read "/path/to/song.mp3": permission denied` | The plugin lacks *Library Access* for the library containing this song. |
| `skipping … – only MP3, FLAC, OGG and Opus files are supported (got .m4a)` | The file is in a container the plugin doesn't support yet. The song is silently passed over. |
| Ratings not updating after I edited a tag | Incremental sync only re-processes files whose mtime moved. Confirm with `ls -l` that your tag editor actually updated the mtime. If it didn't, run a one-off `incremental_sync = false` pass. |
| First run took forever, second run was fast | Working as designed — that's the whole point of incremental sync. |
| `KVStoreGet … failed – falling back to full scan` (warning) | The Navidrome KV store is unreachable for some reason. The current scan still works (as a full scan); investigate the underlying error. |
