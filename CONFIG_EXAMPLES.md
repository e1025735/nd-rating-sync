# nd-rating-sync — Configuration examples

This file goes deeper than [`HOW_TO_SETUP.md`](HOW_TO_SETUP.md): it shows what each setting does to real songs, walks through a few typical setups and says what to look for in the log.
The plugin is configured only on Navidrome's admin page (Avatar → Plugins → nd-rating-sync).

## The settings at a glance

**Top level** (applies to everyone):

| Admin page label | Values | Default |
|------------------|--------|---------|
| Sync schedule (cron expression) | a cron expression | `0 * * * *` (hourly) |
| Incremental sync | on / off | on |
| Dry run | on / off | off |
| Default: skip already-rated songs | No admin default (plugin: true) / Yes / No | No admin default |
| Default: clear rating if no tag found | No admin default (plugin: false) / Yes / No | No admin default |

**Per library** (Library configurations): Library ID (the numeric ID, not the
name), Library display name (only for you), and a list of users.

**Per user** (User settings for this library):

| Admin page label | Values |
|------------------|--------|
| Navidrome username | the login name (case does not matter) |
| Skip already-rated songs | Inherit from admin default / Yes / No |
| Clear rating if no tag found | Inherit from admin default / Yes / No |
| Rating tag priority order | an ordered list of `WMP`, `iTunes`, `MediaMonkey`, `foobar2000`, `MusicBee`, `FMPS` |

"Inherit" takes the top-level default; when that is "No admin default", the
plugin uses **Yes** for skipping and **No** for clearing.

## What happens to a song

For every song, the plugin compares the rating in the file with the rating the
user has in Navidrome. A file is in one of four states:

- **rated**: a source in the user's tag order holds 1–5 whole stars;
- **no rating**: the file was read completely and holds no rating from the
  user's sources, or a rating of 0. Such a file is **rated 0 stars**;
- **half star**: the file is rated, but with a value Navidrome cannot show (a
  half star such as 3½, or a value outside the player's scale);
- **unreadable**: the file is missing, cut off, corrupt or in a format the
  plugin does not read.

### Song without a rating in Navidrome

The skip setting makes no difference here, and the clear setting only
changes how a file without a rating is counted:

| File is… | Result | Counted as |
|----------|--------|------------|
| rated | Navidrome gets the file's rating | `rated` |
| no rating, Clear = Yes | nothing to do: the file's 0 stars equal Navidrome's 0 | `skipped_same_rating` |
| no rating, Clear = No | nothing to do: the missing rating is not used | `skipped_no_tag` |
| half star | left unrated | `skipped_not_whole_star` |
| unreadable | left unrated, with a warning | `skipped_unreadable` |

### Song that already has a rating in Navidrome

| File is… | Skip = Yes | Skip = No, Clear = No | Skip = No, Clear = Yes |
|----------|-----------|-----------------------|------------------------|
| rated, same stars | kept (file not read) | kept | kept |
| rated, other stars | kept (file not read) | **file's rating** | **file's rating** |
| no rating | kept (file not read) | kept | **set to 0** |
| half star | kept (file not read) | kept | kept |
| unreadable | kept (file not read) | kept, with a warning | kept, with a warning |

Counted as: Skip = Yes → `skipped_already_rated`. Otherwise "kept" for the
same stars is `skipped_same_rating`, for no rating (Clear = No) `skipped_no_tag`, for a
half star `skipped_not_whole_star`, for an unreadable file
`skipped_unreadable`; a new rating is `rated`, a rating set to 0 is
`cleared`.

Things to keep in mind:

- **Clear = Yes only matters together with Skip = No.** With Skip = Yes, songs
  rated in Navidrome are skipped before their file is read, so nothing is ever
  cleared. The plugin warns about this combination in the log.
- **A half star or an unreadable file never changes a rating.** Only a file
  the plugin read completely and that holds no rating counts as 0 stars.
- **Incremental sync decides which files are read at all.** After the first
  sync, a file is read again only when it changed since the last sync, when
  Navidrome added the song since then, or once after you change that user's
  tag order, skip or clear setting. A rating you change in Navidrome is
  therefore not overwritten on the next run, only when the file is read
  again. Switch Incremental sync off for one run to read everything.
- **Only the sources in the tag order count.** A rating written by a player
  that is not in the list is not read; for that user the file has no rating.

## How the tag order picks a rating

A file can hold ratings from several players. The plugin takes the first
source in the user's **Rating tag priority order** that holds a whole-star
rating:

| The file holds… | Tag order | Result |
|-----------------|-----------|--------|
| MusicBee POPM ★★★★, WMP POPM ★★ | `MusicBee`, `WMP` | ★★★★ |
| MusicBee POPM ★★★★, WMP POPM ★★ | `WMP`, `MusicBee` | ★★ |
| MusicBee POPM 3½ stars, `FMPS_Rating` 0.8 | `MusicBee`, `FMPS` | ★★★★ (the half star is passed over) |
| MusicBee POPM 3½ stars only | `MusicBee`, `FMPS` | half star: the Navidrome rating is kept |
| `FMPS_Rating` 0.8 only | `MusicBee` | no rating (FMPS is not in the list) |

Which tag each source reads in each file format is listed in
[`DETAILS.md`](DETAILS.md#rating-value-mapping) and the README.

## Scenario 1: ratings live in MusicBee, Navidrome only fills gaps

Alice rates in MusicBee on her PC. Songs she rated in Navidrome on her phone
should stay as they are.

| Library ID | User | Skip already-rated songs | Clear rating if no tag found | Rating tag priority order |
|------------|------|--------------------------|------------------------------|---------------------------|
| `1` | `alice` | Yes | Inherit (→ No) | `MusicBee` |

- Every song Alice has not rated in Navidrome gets MusicBee's rating.
- Songs she rated in Navidrome are never touched, and their files are not
  even read, which keeps the first import fast.
- Half stars from MusicBee (3½) are not synced; those songs stay unrated in
  Navidrome until she gives them a whole star.

## Scenario 2: the files are the master copy

Bob rates everything in foobar2000 and wants Navidrome to mirror his files,
including ratings he removed.

| Library ID | User | Skip already-rated songs | Clear rating if no tag found | Rating tag priority order |
|------------|------|--------------------------|------------------------------|---------------------------|
| `1` | `bob` | No | Yes | `foobar2000` |

- A file rated in foobar2000 sets the Navidrome rating, also over one Bob gave
  in Navidrome.
- A file without a rating sets the Navidrome rating to 0.
- `foobar2000` alone is enough for MP3: foobar2000 writes its rating there as
  the Windows Media Player POPM frame, which this source reads when there is
  no `RATING` field.
- A rating Bob changes in Navidrome stays until the file is read again (see
  *Incremental sync* above). The files win, but only when they change.

## Scenario 3: two people, two players, one library

Carol uses Strawberry (which writes `FMPS_Rating`) and Dave uses MediaMonkey.
Both listen to the same library but keep their own ratings.

| Library ID | User | Skip already-rated songs | Clear rating if no tag found | Rating tag priority order |
|------------|------|--------------------------|------------------------------|---------------------------|
| `1` | `carol` | Yes | Inherit (→ No) | `FMPS` |
| `1` | `dave` | Yes | Inherit (→ No) | `MediaMonkey` |

- Ratings in Navidrome are per user, so each gets the ratings of their own
  player.
- Both players write into the same files, each into its own field. Mind one
  overlap: the `MediaMonkey` source also reads `FMPS_Rating` as a fallback
  when MediaMonkey's own field is missing. So a song Carol rated in Strawberry
  but Dave never rated in MediaMonkey gets Carol's rating for Dave too. There
  is no switch for this; the fallback is part of the `MediaMonkey` source.
- Each user's pass reads the files on its own. The first import of a large
  library takes about twice as long with two users (see *Large libraries* in
  HOW_TO_SETUP).

## Scenario 4: admin defaults for many users

An admin wants the files to win for everyone except one user.

Top level:

| Admin page label | Value |
|------------------|-------|
| Default: skip already-rated songs | No |
| Default: clear rating if no tag found | Yes |

Users of library `1`:

| User | Skip already-rated songs | Clear rating if no tag found | Rating tag priority order |
|------|--------------------------|------------------------------|---------------------------|
| `erin` | Inherit (→ No) | Inherit (→ Yes) | *(empty: the default order)* |
| `frank` | Inherit (→ No) | Inherit (→ Yes) | `WMP`, `MusicBee` |
| `grace` | Yes | Inherit (→ Yes) | `iTunes` |

- Erin and Frank mirror their files, including cleared ratings.
- Grace keeps everything she rated in Navidrome. Her "Clear" inherits Yes but
  has no effect together with Skip = Yes, and the log warns about that
  combination when her songs are synced.
- An empty tag order means the default: `WMP`, `iTunes`, `MediaMonkey`,
  `foobar2000`, `MusicBee`, `FMPS`.

## Checking a setup with Dry run

Turn **Dry run** on, save, and watch the `nd-rating-sync:` lines in the
Navidrome log. Nothing is written, and the incremental-sync timestamp is not
saved, so the first real run reads everything again.

For each song that would change you see one line, for example:

```
nd-rating-sync: [DRY RUN] would rate "Song title" → 4 stars
nd-rating-sync: [DRY RUN] would clear rating for "Other song" (no tag found)
```

and for each time slice a summary per user:

```
nd-rating-sync: [DRY RUN] chunk done user="bob" library="1" – would_rate=120 would_clear=8 skipped_already_rated=0 skipped_same_rating=310 skipped_unchanged=0 skipped_no_tag=42 skipped_not_whole_star=3 skipped_unreadable=1
```

How to read it:

- `would_clear` is above 0 only with Skip = No and Clear = Yes. If it is
  higher than you expect, check the tag order: a rating from a player that is
  not in the list counts as no rating.
- `skipped_not_whole_star` are half stars or out-of-range values; their
  Navidrome rating is kept.
- `skipped_unreadable` comes with a warning per file that says why. Such a
  file is never changed.
- `skipped_already_rated` is everything Skip = Yes protected.
- A large library runs in slices (`time budget reached – rescheduled
  continuation …`); it is done at `sync complete`.

When the numbers look right, turn Dry run off.
