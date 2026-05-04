# nd-rating-sync

A [Navidrome](https://www.navidrome.org/) plugin that reads embedded star-rating
tags from your music files and syncs them into Navidrome's own user-rating
system via the internal Subsonic API.

## Why this plugin exists

Navidrome already reads several tag fields during library scan, but it does
**not** automatically convert them into the clickable ★★★★★ user rating that
appears in the UI and is queryable via smart playlists.  This plugin closes that
gap: it periodically scans every song, reads the embedded rating tag, and calls
`setRating` so the value is reflected in Navidrome just as if you had clicked
the stars yourself.

---

## Supported tag formats

| Container | Tag field | Notes |
|-----------|-----------|-------|
| MP3 (ID3v2.3 / v2.4) | `TXXX:FMPS_Rating` | Float 0.0–1.0 written by fre:ac, MusicBee, beets, etc. |
| MP3 (ID3v2.3 / v2.4) | `POPM` (Popularimeter) | Byte 0–255; Winamp/MediaMonkey mapping |
| FLAC | Vorbis comment `FMPS_RATING` | Float 0.0–1.0 |
| OGG Vorbis | Vorbis comment `FMPS_RATING` | Float 0.0–1.0 |
| Opus | Vorbis comment `FMPS_RATING` | Float 0.0–1.0 (OpusTags packet) |
| M4A / MP4 / AAC | iTunes `rtng` atom | 0/20/40/60/80/100 |
| M4A / MP4 / AAC | Freeform `----:com.apple.iTunes:FMPS_Rating` | Float 0.0–1.0 |

### Rating value mapping

**FMPS_Rating (float 0.0–1.0 → ★)**

| Tag value | Navidrome stars |
|-----------|-----------------|
| ≤ 0.0 or missing | (skip) |
| 0.01 – 0.20 | ★ |
| 0.21 – 0.40 | ★★ |
| 0.41 – 0.60 | ★★★ |
| 0.61 – 0.80 | ★★★★ |
| 0.81 – 1.00 | ★★★★★ |

**POPM byte (0–255 → ★)**

| Byte range | Stars | Software convention |
|------------|-------|---------------------|
| 0 | (skip) | unrated |
| 1–63 | ★ | Winamp 1★ |
| 64–127 | ★★ | Winamp 2★ |
| 128–191 | ★★★ | Winamp 3★ |
| 192–223 | ★★★★ | Winamp 4★ |
| 224–255 | ★★★★★ | Winamp 5★ |

**iTunes rtng byte**

| Byte | Stars |
|------|-------|
| 0 | (skip) |
| 1–20 | ★ |
| 21–40 | ★★ |
| 41–60 | ★★★ |
| 61–80 | ★★★★ |
| 81–100+ | ★★★★★ |

---

## Prerequisites

- Navidrome ≥ 0.60.0 (plugin system required)
- [TinyGo](https://tinygo.org/getting-started/install/) for building
- `zip` utility for packaging

## Build

```sh
# 1. Clone this repo
git clone https://github.com/yourusername/nd-rating-sync
cd nd-rating-sync

# 2. Point go.mod at your local Navidrome PDK (see go.mod comments)
#    or use a published pseudo-version once available.

# 3. Build and package
make
# Produces: nd-rating-sync.ndp
```

## Installation

1. Copy `nd-rating-sync.ndp` to your Navidrome plugins folder
   (default: `<DataFolder>/plugins`).

2. Enable plugins in your Navidrome config:

   ```toml
   [Plugins]
   Enabled    = true
   AutoReload = true   # convenient during development
   LogLevel   = "info"
   ```

3. Open the Navidrome web UI → click your avatar → **Plugins**.

4. Click **Rescan**, then enable **nd-rating-sync**.

5. Click the plugin row to open its settings and configure:

   | Key | Description |
   |-----|-------------|
   | `username` | The Navidrome account whose ratings will be updated |
   | `skip_already_rated` | `true` (default) – skip songs already rated in Navidrome |
   | `max_songs_per_run` | Max songs processed per scheduled run (default `500`) |

6. In **User Access**, add the same user you entered as `username`.

7. In **Library Access**, grant access to all libraries (or the specific ones
   you want synced).

The plugin runs an immediate scan on startup, then repeats every 6 hours.

## How it works

```
Navidrome startup
      │
      ▼
nd_lifecycle_on_init()
      │  schedules recurring job (every 6 h)
      │  schedules one-shot job (now)
      │
      ▼
nd_scheduler_callback()   ◄─── fires every 6 hours
      │
      ├─ SubsonicAPICall("search3") ──► paginate all songs
      │
      └─ for each unrated* song:
             │
             ├─ os.ReadFile(song.Path)   ← library.filesystem permission
             │
             ├─ parse tags (ID3v2 / Vorbis / MP4)
             │
             └─ SubsonicAPICall("setRating?id=...&rating=...")
```

`*` When `skip_already_rated = true`, songs that already have a Navidrome user
rating are skipped even if the file tag differs.

## Troubleshooting

Enable debug logs in `[Plugins] LogLevel = "debug"` and filter for
`plugin=nd-rating-sync` in the Navidrome log output.

Common issues:

- **"username is not configured"** – set the `username` config key in the
  plugin settings UI.
- **setRating API error 50** – the plugin user isn't listed under **User
  Access**.  Add them in the Navidrome plugin UI.
- **Files not found** – make sure the plugin has **Library Access** configured
  for the relevant libraries.
- **No ratings applied** – check that your files actually contain one of the
  supported tag fields (use a tag editor to verify).
