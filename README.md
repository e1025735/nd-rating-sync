# nd-rating-sync

A [Navidrome](https://www.navidrome.org/)  plugin that reads embedded star-rating tags from supported music files and syncs them into Navidrome's user rating system via the internal Subsonic API.

**Warning: This is still in beta!** Start with `dry_run=true` and verify the first import before allowing the plugin to write ratings.

## What it does

- Reads embedded ratings from supported audio containers.
- Matches Navidrome songs to files by exact size + extension.
- Writes ratings via the Subsonic `setRating` API.
- Supports per-library, per-user tag priority and overwrite behavior.
- Runs inside Navidrome's plugin sandbox using library filesystem mounts.

## Supported formats

- MP3
- FLAC
- Ogg / Opus
- WAV
- DSF
- M4A / AAC
- WMA

## Supported rating sources

The plugin supports these embedded rating sources:

- `MediaMonkey`
- `foobar2000`
- `WMP` (Windows Media Player; also used by MusicBee)
- `iTunes`

The order in `ratingTagOrder` determines which tag wins when a file contains multiple rating formats.

For detailed rating value mappings, see [`DETAILS.md`](DETAILS.md).

## Important notes

- `libraryId` must be the numeric Navidrome library ID, not the display name.
- `username` must exactly match the Navidrome username.
- Each configured user must also be granted access under the plugin's **User Access** tab.
- Each configured library must be assigned to the plugin and granted filesystem access.
- The plugin only reads files; it does not write metadata back to your music library.
- Navidrome may label filesystem access as library permission with "write access". This is a host permission label, not an indication that the plugin writes files.

## Slow / large libraries

If your library is very large or hosted on slow storage, the normal filesystem walk can take too long.
In that case, enable `Cache Libraries Filesystem Tree` and set `kv_storage_max_size` to match `permissions.kvstore.maxSize` in `manifest.json`.

## Full setup instructions

See [`HOW_TO_SETUP.md`](HOW_TO_SETUP.md) for detailed installation and configuration information, including normal setup and cache setup.

## Detailed behavior and rating mapping

See [`DETAILS.md`](DETAILS.md) for runtime behavior and rating value mapping details.

## Troubleshooting

- Start with `dry_run=true`.
- If every file is skipped as unreadable, verify library mount permissions and plugin assignment.
- If the first scan stalls during the directory walk, enable the cache and increase KV storage sizing.
- If `setRating` fails with API error 50, the user is not authorised in plugin User Access.

## Notes

- The first full scan can be slow for large libraries or remote mounts.
- The plugin breaks work into budgeted chunks to stay safely under Navidrome's 30-second callback limit.
- The plugin relies on file size + extension matching because Navidrome does not provide a usable filesystem path for songs inside the sandbox.
