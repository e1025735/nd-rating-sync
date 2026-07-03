# nd-rating-sync — Setup Guide

This guide explains how to install, configure, and run `nd-rating-sync` in Navidrome.
It includes both the normal setup path and the optional filesystem tree cache option for very large or slow libraries.

## What this plugin does

`nd-rating-sync` reads embedded star-rating tags from supported music files and syncs them into Navidrome's user rating system using the Subsonic `setRating` API.
It is designed to preserve ratings stored in file metadata formats such as FMPS_Rating, POPM, RATING, and WMA shared-user ratings.

## Supported file formats

- MP3: ID3v2 (TXXX, POPM)
- FLAC: Vorbis comments
- Ogg / Opus: Vorbis comments
- WAV: ID3v2 chunk inside RIFF
- DSF: ID3v2 block in the DSD header
- M4A / AAC: MP4 freeform atoms
- WMA: ASF Extended Content Description

Unsupported files are skipped without clearing ratings.

## Prerequisites

- Navidrome ≥ 0.60.0
- A plugin-capable Navidrome build with plugin permissions enabled
- `zip` utility for packaging the plugin
- TinyGo for building from source (only if you build the plugin yourself)

## Install the plugin

1. Get the plugin (either is fine)

   1. Download the newest release version from github

   2. Build and package the plugin into `nd-rating-sync.ndp`.
   - `make`
   - This produces `nd-rating-sync.ndp`.

2. Copy `nd-rating-sync.ndp` into Navidrome's plugins folder.

3. Enable plugins in Navidrome config:

```toml
[Plugins]
Enabled    = true
AutoReload = true
LogLevel   = "info"
```

4. In the Navidrome UI: Avatar → Plugins → Rescan (if still needed)

5. Adapt the settings of the plugin and save them

6. Enable `nd-rating-sync` 

## Permissions and library access

The plugin needs Navidrome to grant it the `library` permission with `filesystem: true`.
This allows Navidrome to mount each assigned library inside the plugin sandbox.

Important:

- The plugin itself only reads files and does not write to the library filesystem.
- Navidrome may label this permission as library access with "write access".
  That is a Navidrome-side permission name, not an indication that the plugin writes metadata.
- The plugin must be assigned the library in Navidrome's plugin library access settings.
- Every configured user must also be granted access under the plugin's User Access tab.

If the plugin cannot resolve the mount point or the library is not assigned, every file will be skipped as unreadable.

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

A bigger example can be seen in [`config.example.json`](config.example.json)

### Top-level admin settings

| Setting | Default | Meaning |
|---------|---------|---------|
| `sync_schedule` | `0 * * * *` | Cron expression for recurring scans. Frequent schedules are cheap because unchanged libraries are skipped by the change-detection gate. |
| `incremental_sync` | `true` | When enabled, recurring scans skip files whose mtime predates the last successful scan timestamp. Set to `false` to force a full scan. |
| `dry_run` | `false` | When enabled, the plugin reads files and evaluates every song but does not call `setRating`. Use this first to verify behavior. |
| `default_skip_already_rated` | `inherit` | Admin-level fallback for per-user `skip_already_rated`. If unset, the plugin defaults to true. |
| `default_clear_rating_if_untagged` | `inherit` | Admin-level fallback for per-user `clear_rating_if_untagged`. If unset, the plugin defaults to false. |

### Library settings

Each entry in `libraries` must include:

- `libraryId` — the numeric Navidrome library ID, not the display name.
- `libraryName` — optional label for your reference.
- `users` — an array of per-user settings.

### User settings

Each user entry must include `username`.
Additional options:

- `skip_already_rated`
  - `yes`: leave already-rated Navidrome songs untouched.
  - `no`: allow file tag values to overwrite existing Navidrome ratings.
  - `inherit`: use the admin default.

- `clear_rating_if_untagged`
  - `yes`: clear the Navidrome rating when the file has no recognised rating tag.
  - `no`: leave unrated files alone.
  - `inherit`: use the admin default.

- `ratingTagOrder`
  - Ordered list of tag sources to try.
  - First match wins.
  - Supported values: `WMP`, `iTunes`, `MediaMonkey`, `foobar2000`.

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

For example:

- `MediaMonkey` covers FMPS_Rating tags in MP3/WAV/DSF and Vorbis comments in FLAC/Ogg/Opus.
- `foobar2000` covers plain RATING tags.
- `WMP` and `iTunes` cover their respective POPM/ASF representations. `WMP` also matches MusicBee's WMP-style rating tags.

This allows each user to mirror their own tagging workflow and keeps the first valid tag from taking precedence.

# How to setup

## 1: Normal setup flow

1. Build/package `nd-rating-sync.ndp`
2. Upload the NDP to Navidrome
3. Open settings of the plugin
4. Grant plugin User Access for each configured username
5. Grant plugin Library Access for each configured library
6. Configure the plugin as desired
7. Start with `dry_run=true`
8. Enable the plugin
9. Wait for the first scan to complete, then inspect logs
10. If results look good, set `dry_run=false` and let is work for real

## 2: Cache Libraries Filesystem Tree (slow/huge libraries)

If your library is very large or hosted on a slow filesystem such as a CIFS share, the normal filewalk can itself take longer than the 30-second budget.

1. Build/package `nd-rating-sync.ndp`
2. Adjust the ndp file
    1. The ndp file is like a zip file and contains manifest.json and a plugin.wasm files
    2. Edit the permissions.kvstore.maxSize in manifest.json to something more fitting for your library. Maybe 150MB?

3. Upload the NDP to Navidrome
4. Open settings of the plugin
5. Grant plugin User Access for each configured username
6. Grant plugin Library Access for each configured library
7. Configure the plugin as desired
8. Enable Cache Libraries Filesystem Tree and set KV storage max size to the same value as the permissions.kvstore.maxSize before. In this example 150MB
9. Start with `dry_run=true`
10. Enable the plugin
11. Wait for the first scan to complete, then inspect logs
12. If results look good, set `dry_run=false` and let is work for real

In that case, enable `Cache Libraries Filesystem Tree` in the plugin settings.

### When to enable it

- Your initial scan stalls or times out while scanning the library tree.
- You are using a slow remote mount and the plugin spends a long time walking directories.
- You have tens of thousands of files and repeated scans re-walk the same tree.

### What it does

When enabled, the plugin caches the discovered library file tree in Navidrome's KV store.
That means the slow filesystem walk is not repeated from scratch on every scan.

### Required KV configuration

The plugin setting `kv_storage_max_size` must match the manifest permission `permissions.kvstore.maxSize`.

For example, if you set:

```json
"permissions": {
  "kvstore": {
    "reason": "...",
    "maxSize": "150MB"
  }
}
```

then also set:

```json
"kv_storage_max_size": "150MB"
```

If these values differ, the plugin may not be able to use log the cache usage correctly.

### Navidrome host cache sizing

Navidrome's plugin system also has a global plugin cache limit. The default is often `200MB`.
If your plugin manifest requests more KV storage than the host allows, you may need to increase Navidrome's `Plugins.CacheSize` setting.

### Important note

`Cache Libraries Filesystem Tree` is an optimization for slow filesystem walks. It does not change how the plugin resolves files by size and extension, and it does not alter rating parsing logic.

## Important caution for huge scans

The plugin currently processes each configured user separately within the same library.
On a very large library, the initial scan for multiple users can take a long time, especially on slow remote storage.

Start with one user and one library first, verify the results, and then add more users once the base scan works.

# After setup

- If you enabled `dry_run`, switch it off once you are confident the plugin is matching ratings correctly.
- Watch the Navidrome logs for `nd-rating-sync:` messages.
- Confirm that ratings appear in the Navidrome UI after the first successful scan.

# Misc
## Common config gotchas

- `libraryId` must be the numeric Navidrome library ID.
- `username` must exactly match the Navidrome username.
- `User Access` and `Library Access` must both be granted in Navidrome for the plugin to work.
- If the plugin is assigned a library but the filesystem mount is not available, everything will be skipped as unreadable.
- If `cache_libraries_filesystem_tree` is enabled, `kv_storage_max_size` must be updated and match the manifest.

## Recommended first run

- Set `dry_run=true`.
- Keep `incremental_sync=true`.
- Use an hourly or more frequent cron schedule such as `0 * * * *` or `*/15 * * * *`.
- Check logs after the first run and make sure the plugin completes or reschedules with a continuation.

If the plugin repeatedly fails at the 30-second boundary during the library walk, enable the filesystem tree cache and increase KV store sizing.

## How the plugin works

- `nd-rating-sync` uses the library filesystem mount point provided by Navidrome to read files.
- It matches songs to files by exact byte size and file extension. If there are multiple files with the same size and extension then no change will be made by the plugin.
- The plugin reads only supported containers, parses the embedded rating tags, and calls `setRating` for the matched Navidrome song.
- Navidrome closes plugin callbacks that exceed 30 seconds, so the plugin runs in short time-budgeted chunks.
- If the budget is reached before completion, the plugin reschedules a continuation and resumes where it left off.

For the full runtime flow and diagram, see [`DETAILS.md`](DETAILS.md).

## Why build a one-shot run around 30 seconds?

Navidrome kills plugin callbacks that exceed 30 seconds. The plugin therefore limits each invocation to a conservative budget and continues in a fresh callback.

This means a large first scan may take many chained callbacks, but it will complete without hitting the hard 30-second host timeout.
