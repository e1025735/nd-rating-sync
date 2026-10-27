# nd-rating-sync — Setup Guide

This guide explains how to install, configure, and run `nd-rating-sync` in Navidrome.

## What this plugin does

`nd-rating-sync` reads embedded star-rating tags from supported music files and syncs them into Navidrome's user rating system using the Subsonic `setRating` API.
It is designed to preserve ratings stored in file metadata formats such as FMPS_Rating, POPM, RATING, and WMA shared-user ratings.

## Supported file formats

- MP3: ID3v2 (TXXX, POPM)
- FLAC: Vorbis comments
- Ogg / Opus: Vorbis comments
- WAV: ID3v2 chunk inside RIFF
- DSF: ID3v2 block in the DSD header
- M4A / MP4: MP4 freeform atoms
- AAC (`.aac`): an ADTS stream with a leading ID3v2 tag (read like MP3), or an MP4 file with that extension (read like M4A). ADTS files without an ID3v2 tag are skipped as unreadable: their tags are usually APEv2, which the plugin does not read
- WMA: ASF Extended Content Description

Unsupported files are skipped without clearing ratings. The same holds for any file the plugin cannot read to a clean end (wrong format for its extension, cut off, corrupt): it is counted as unreadable, and only a file read completely without a rating tag can have its rating cleared.

## Prerequisites

- Navidrome ≥ 0.63.0 (the plugin uses Navidrome's matcher service, which older versions do not have; on older versions the plugin cannot be loaded)
- A plugin-capable Navidrome build with plugins enabled
- To build from source only: TinyGo and the `zip` utility

## Install the plugin

1. Get `nd-rating-sync.ndp`, either way:
   - download it from the newest release on GitHub, or
   - build it yourself with `make`, which produces `nd-rating-sync.ndp`.
2. Copy `nd-rating-sync.ndp` into Navidrome's plugins folder.
3. Enable plugins in the Navidrome config:

   ```toml
   [Plugins]
   Enabled    = true
   AutoReload = true
   LogLevel   = "info"
   ```

4. In the Navidrome UI: Avatar → Plugins → Rescan (if the plugin does not show up yet).

## Permissions and library access

The plugin needs Navidrome to grant it the `library` permission with `filesystem: true` and the `matcher` permission.
Filesystem access lets Navidrome mount each assigned library read-only inside the plugin sandbox, and it is also what makes the matcher return each song's path inside its library.

- The plugin only reads files and never writes to the library filesystem.
- Navidrome may label this permission as library access with "write access".
  That is a Navidrome-side permission name, not an indication that the plugin writes metadata.
- Every configured library must be granted under the plugin's **Library Access** tab.
- Every configured user must be granted under the plugin's **User Access** tab.

Before a user's sync starts, the plugin checks the username against the users granted under User Access and the library against Library Access, and skips a mismatch at once with a warning that says which one. If the plugin cannot resolve a library's mount point, that library is skipped with a warning and no rating is touched.

## Configure the plugin

The plugin uses a hierarchical configuration model.
Top-level admin settings apply globally, while each library lists one or more users with per-user preferences.

### Minimal config

The only required top-level field is `libraries`.
Each library entry requires a numeric `libraryId`.

```json
{
  "libraries": [
    {
      "libraryId": "1",
      "libraryName": "Main Music",
      "users": [
        {
          "username": "alice",
          "skip_already_rated": "yes",
          "clear_rating_if_untagged": "inherit",
          "ratingTagOrder": ["MediaMonkey", "foobar2000"]
        }
      ]
    }
  ]
}
```

A bigger, annotated example is in [`config.example.json`](config.example.json).

### Top-level admin settings

| Setting | Default | Meaning |
|---------|---------|---------|
| `sync_schedule` | `0 * * * *` | Cron expression for recurring scans. Frequent schedules are cheap because unchanged libraries are skipped by the change-detection gate. |
| `incremental_sync` | `true` | When enabled, recurring scans skip files whose mtime predates the last successful scan timestamp. Set to `false` to force a full scan. |
| `dry_run` | `false` | When enabled, the plugin reads files and evaluates every song but does not call `setRating`. Use this first to verify behavior. |
| `default_skip_already_rated` | `inherit` | Admin-level fallback for per-user `skip_already_rated`. If unset, the plugin defaults to true. |
| `default_clear_rating_if_untagged` | `inherit` | Admin-level fallback for per-user `clear_rating_if_untagged`. If unset, the plugin defaults to false. |

### Library settings

Each entry in `libraries` has:

- `libraryId` — the numeric Navidrome library ID, not the display name (required).
- `libraryName` — optional label for your reference.
- `users` — an array of per-user settings.

### User settings

Each user entry must include `username`, the Navidrome login name (case does not matter).
Additional options:

- `skip_already_rated`
  - `yes`: leave already-rated Navidrome songs untouched.
  - `no`: allow file tag values to overwrite existing Navidrome ratings.
  - `inherit`: use the admin default.

- `clear_rating_if_untagged`
  - `yes`: clear the Navidrome rating when the file has no recognised rating tag. Songs skipped by `skip_already_rated` are not read, so their ratings are never cleared.
  - `no`: leave unrated files alone.
  - `inherit`: use the admin default.

- `ratingTagOrder`
  - Ordered list of tag sources to try; the first one found in the file wins.
  - Supported values: `WMP`, `iTunes`, `MediaMonkey`, `foobar2000`, `MusicBee`, `FMPS`.
  - Default (when left empty): `WMP`, `iTunes`, `MediaMonkey`, `foobar2000`, `MusicBee`, `FMPS`.

Example:

```json
{
  "username": "bob",
  "skip_already_rated": "no",
  "clear_rating_if_untagged": "yes",
  "ratingTagOrder": ["foobar2000", "WMP", "iTunes"]
}
```

### How `ratingTagOrder` works

The plugin tries the configured sources in order and uses the first tag found for that file.
Sources that do not exist in a container are simply skipped.

- `MediaMonkey` covers MediaMonkey's own fields (POPM `no@email` in MP3/WAV/DSF, `RATING` in FLAC/Ogg/Opus, `rate` in M4A) and, as a fallback, FMPS_Rating tags.
- `foobar2000` covers plain RATING tags and, in MP3, the Windows Media Player POPM frame foobar2000 writes there.
- `FMPS` covers the freedesktop `FMPS_Rating` field (Strawberry, Clementine, Amarok and others) in every container.
- `WMP` and `iTunes` cover their respective POPM/ASF representations. `iTunes` means iTunes-style tags written by other tools. iTunes and Apple Music keep ratings in their library, not in the file, so a rating set in iTunes is never in the file.
- `MusicBee` covers MusicBee's POPM frame, `RATING` (FLAC/Ogg/Opus), `RATE` (M4A) and `WM/SharedUserRating` (WMA).

Half-star values are never synced. This lets each user mirror their own tagging workflow.

## First run

1. Grant the plugin User Access for each configured username and Library Access for each configured library.
2. Configure the plugin and set `dry_run=true`. Keep `incremental_sync=true` and an hourly or more frequent schedule such as `0 * * * *` or `*/15 * * * *`.
3. Enable `nd-rating-sync`.
4. Wait for the first scan to complete (`sync complete` in the log), then check the `nd-rating-sync:` lines. A large library runs as a chain of continuations (`time budget reached – rescheduled continuation`) before it completes.
5. If the results look good, set `dry_run=false` and let it work for real. Confirm that ratings appear in the Navidrome UI after the next scan.

Start with one user and one library, verify the results, and then add more users once the base scan works.

## Large libraries

The plugin processes each configured user separately within the same library, and each user's pass reads the files again. Whenever files are actually read, the cost is **one file read per song per user**:

- the first import,
- every run with `incremental_sync=false`,
- the one full re-read after a user's `ratingTagOrder`, `skip_already_rated` or `clear_rating_if_untagged` changes,
- the files Navidrome found changed in a rescan.

For example, 50,000 songs with 3 users on a network share at about 10 ms per file take roughly 25 minutes for the first import instead of about 8 minutes with one user. Normal runs stay cheap: an unchanged library is skipped without listing it, and unchanged files are skipped without reading them.

## Common config gotchas

- `libraryId` must be the numeric Navidrome library ID.
- `username` must match the Navidrome login name (case does not matter).
- `User Access` and `Library Access` must both be granted in Navidrome for the plugin to work.
- If the plugin is assigned a library but the filesystem mount is not available, the library is skipped with a warning.
- If songs are skipped with `cannot locate the file of …`, the rest of the warning says why (no track for the song ID, file missing, no path because filesystem access is not granted, or the file is not in the mount).

## Incremental sync and file times

With `incremental_sync=true` (the default) a file is only read again when its modification time is newer than the start of the last completed sync. That start time comes from the Navidrome server's clock; the file's modification time comes from wherever the file is stored, or, on a network share, often from the computer that wrote the tag. A changed rating can therefore be missed for good:

- **Clocks out of step.** If the file server or your desktop clock is behind the Navidrome server, a file you re-rated after the last sync can look older than it. Keep every machine on NTP (automatic time sync).
- **Wrong timezone or DST settings.** A NAS whose clock is set to local time but reported as UTC, or FAT/exFAT and some older SMB servers that shift file times by an hour around daylight saving time, can make file times hours off. Set the NAS to UTC or its correct timezone and keep it consistent.
- **Taggers that keep the file time.** Some tag editors can preserve the original modification time when writing tags (Mp3tag has such an option). Then neither Navidrome nor this plugin can see that the file changed. Turn that option off for rating changes.

If ratings you changed in a file do not show up, run one sync with `incremental_sync=false` (or change and restore a user's `ratingTagOrder`) to read every file once, then switch it back on.

## How the plugin works

- For each page of songs the plugin asks Navidrome's matcher for the songs' paths (by song ID) and opens each file at that path inside the library's read-only mount. There is no directory walk and no file index.
- The plugin reads only the tag area of supported containers, parses the embedded rating tags, and calls `setRating` for the song.
- Navidrome closes plugin callbacks that exceed 30 seconds, so the plugin works in 15-second chunks. If a chunk ends before the sync is complete, the plugin schedules a continuation that resumes where it left off, so a large first scan runs as many chained callbacks.
- If a step fails without progress (the song listing or the matcher lookup fails), it retries after 30 seconds and gives up on that library/user after three attempts until the next scheduled sync.
- A file whose tags cannot be read or parsed is skipped as unreadable; its rating is never cleared.

For the full runtime flow and diagram, see [`DETAILS.md`](DETAILS.md).
