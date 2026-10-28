# nd-rating-sync

A [Navidrome](https://www.navidrome.org/)  plugin that reads embedded star-rating tags from supported music files and syncs them into Navidrome's user rating system via the internal Subsonic API.

**Warning: This is still in beta!** Start with `dry_run=true` and verify the first import before allowing the plugin to write ratings.

**Requires Navidrome 0.63.0 or newer** (the plugin uses Navidrome's matcher service to find each song's file). On older Navidrome versions the plugin cannot be loaded; stay on nd-rating-sync 0.22.x there.

## What it does

- Reads embedded ratings from supported audio containers.
- Finds each song's file through Navidrome's matcher (by song ID, using the path Navidrome stored for it).
- Writes ratings via the Subsonic `setRating` API.
- Supports per-library, per-user tag priority and overwrite behavior.
- Runs inside Navidrome's plugin sandbox using library filesystem mounts.

## Supported formats

- MP3
- FLAC
- Ogg / Opus
- WAV
- DSF
- M4A / MP4
- AAC (`.aac`: ADTS with a leading ID3v2 tag, or an MP4 file)
- WMA

## Supported rating sources

The plugin supports these embedded rating sources:

- `MediaMonkey`
- `foobar2000` (in MP3 also the Windows Media Player rating frame foobar2000 writes there)
- `MusicBee`
- `WMP` (Windows Media Player; MusicBee uses the same field in WMA files)
- `iTunes` (iTunes-style tags written by other tools. iTunes and Apple Music keep ratings in their library, not in the file, so a rating set in iTunes is never in the file)
- `FMPS` (the freedesktop `FMPS_Rating` field written by Strawberry, Clementine, Amarok and others)

The order in `ratingTagOrder` determines which tag wins when a file contains multiple rating formats. The default order is `WMP`, `iTunes`, `MediaMonkey`, `foobar2000`, `MusicBee`, `FMPS`.

A file that holds no rating from these sources (or a rating of 0) is rated 0 stars: with `clear_rating_if_untagged` on, its Navidrome rating is set to 0 as well. A file rated with a half star or an out-of-range value keeps its Navidrome rating, since Navidrome has no half stars. Files the plugin cannot read are never touched.

If your player's ratings are not found, please [open an issue](https://github.com/e1025735/nd-rating-sync/issues) and attach a small sample file rated by that player (or the output of a tag viewer such as Mp3tag's extended tags), and name the player and its version. Player rating formats are poorly documented, so real files are the only reliable source.

For detailed rating value mappings, see [`DETAILS.md`](DETAILS.md).

## Important notes

- `libraryId` must be the numeric Navidrome library ID, not the display name.
- `username` must match the Navidrome login name (case does not matter).
- Each configured user must also be granted access under the plugin's **User Access** tab.
- Each configured library must be assigned to the plugin and granted filesystem access. Filesystem access is also what makes Navidrome return each song's path to the plugin.
- The plugin only reads files; it does not write metadata back to your music library.
- Navidrome may label filesystem access as library permission with "write access". This is a host permission label, not an indication that the plugin writes files.

## Full setup instructions

See [`HOW_TO_SETUP.md`](HOW_TO_SETUP.md) for detailed installation and configuration information, and [`CONFIG_EXAMPLES.md`](CONFIG_EXAMPLES.md) for example setups and what each setting does to a song.

## Detailed behavior and rating mapping

See [`DETAILS.md`](DETAILS.md) for runtime behavior and rating value mapping details.

## Troubleshooting

- Start with `dry_run=true`.
- If every file is skipped as unreadable, verify library mount permissions and plugin assignment. The warning `cannot locate the file of …` names the reason for each song.
- If the log says `user "…" is not known to Navidrome or not granted under the plugin's User Access tab`, fix the username or grant the user under the plugin's User Access tab. `library N does not exist or is not assigned to the plugin` means the same for Library Access.
- If a user is skipped after three failed attempts to list their songs, check that the username matches Navidrome exactly.
- Files whose tags cannot be read or parsed are counted as unreadable; their ratings are never cleared.
- If `setRating` fails with API error 50, the user is not authorised in plugin User Access.
- If a rating you changed in a file does not show up, the file's modification time may look older than the last sync (clock skew between machines, timezone/DST settings on a NAS, or a tag editor that keeps the original file time). See [Incremental sync and file times](HOW_TO_SETUP.md#incremental-sync-and-file-times); one run with `incremental_sync=false` reads every file again.
- If the log says `the previous sync stopped unexpectedly`, the plugin crashed or ran into Navidrome's 30-second limit. It resumes there and logs every file it reads on that page (`reading … after an unexpected stop`). If it stops again, the last of those lines names the file causing it: fix or remove that file.

## Notes

- The first full scan can be slow for large libraries or remote mounts.
- The plugin breaks work into budgeted chunks to stay safely under Navidrome's 30-second callback limit.
- The Subsonic API only gives plugins a made-up song path, so the plugin asks Navidrome's matcher for the real path (relative to the library) and opens the file through the library's read-only mount.
- Upgrading from 0.22.x: the settings `cache_libraries_filesystem_tree` and `kv_storage_max_size` are gone, and the file index older versions kept in the KV store is deleted when the plugin loads.
