package scanner

import (
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/e1025735/nd-rating-sync/internal/logging"
	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/types"
)

// songFile is where one song's file was found inside the library mount, or
// why it could not be located.
type songFile struct {
	entry  FileEntry
	found  bool
	reason string // set when !found, for the warning
}

// locateSongFiles resolves the files of a page of songs through Navidrome's
// matcher. The Subsonic `path` is a synthesized fake unless the player has
// "Report Real Path" on, which a plugin cannot set, so the matcher is the
// only source of a song's real location: matching by song ID returns the
// track's path relative to its library root, which joined to the mount gives
// the file inside the sandbox.
//
// Songs skipped as already rated never need their file and are left out. A
// matcher failure is returned as an error (the caller retries the page);
// a song that cannot be located is reported per song.
func locateSongFiles(mount libraryMount, u UserConfig, songs []subsonicSong) (map[string]songFile, error) {
	refs := make([]types.SongRef, 0, len(songs))
	for _, s := range songs {
		if u.SkipAlreadyRated && s.UserRating > 0 {
			continue
		}
		refs = append(refs, types.SongRef{ID: s.ID})
	}
	files := make(map[string]songFile, len(refs))
	if len(refs) == 0 {
		return files, nil
	}

	logging.Trace(fmt.Sprintf("nd-rating-sync: locateSongFiles matching %d songs in library=%d", len(refs), mount.id))
	// No username: the per-user annotations are not needed, and without one
	// the lookup is limited only to the libraries the plugin may access.
	tracks, err := host.MatcherMatchSongs(refs, host.MatchOptions{})
	if err != nil {
		return nil, fmt.Errorf("matcher: %w", err)
	}
	for i, ref := range refs {
		var t *types.Track
		if i < len(tracks) {
			t = tracks[i]
		}
		files[ref.ID] = locateTrack(mount, ref.ID, t)
	}
	return files, nil
}

// locateTrack turns one matcher result into the song's file inside the mount.
func locateTrack(mount libraryMount, songID string, t *types.Track) songFile {
	switch {
	case t == nil || t.ID != songID:
		// Only an ID was sent, so the matcher has nothing to fall back on: the
		// song is gone or outside the libraries the plugin may access.
		return songFile{reason: "Navidrome returned no track for this song ID"}
	case t.Missing:
		return songFile{reason: "Navidrome marks its file as missing"}
	case t.LibraryID != mount.id:
		return songFile{reason: fmt.Sprintf("it belongs to library %d", t.LibraryID)}
	case t.Path == "":
		return songFile{reason: "Navidrome returned no path (grant the 'library' permission with filesystem access)"}
	}
	rel, ok := relativeTrackPath(mount.hostPath, t.Path)
	if !ok {
		return songFile{reason: fmt.Sprintf("its path %q is not inside the library root %q", t.Path, mount.hostPath)}
	}
	full := path.Join(mount.mountPoint, rel)
	info, err := os.Stat(full)
	if err != nil {
		return songFile{reason: fmt.Sprintf("cannot stat %q: %q", full, err.Error())}
	}
	return songFile{entry: FileEntry{Path: full, MTime: info.ModTime()}, found: true}
}

// relativeTrackPath turns a track path from the matcher into a clean,
// slash-separated path relative to the library root. Navidrome stores paths
// relative to the root; an absolute path is accepted only below hostRoot.
// Paths written by a Navidrome running on Windows use backslashes, which are
// converted when the library root shows that the host is Windows (on other
// hosts a backslash is a legal file-name character). A path that would leave
// the root ("..") is rejected.
func relativeTrackPath(hostRoot, p string) (string, bool) {
	windows := strings.Contains(hostRoot, `\`) || hasDriveLetter(hostRoot)
	if windows {
		hostRoot = strings.ReplaceAll(hostRoot, `\`, "/")
		p = strings.ReplaceAll(p, `\`, "/")
	}
	root := strings.TrimRight(hostRoot, "/")
	switch {
	case windows && hasDriveLetter(p):
		// Drive letters and paths compare case-insensitively on Windows.
		if root == "" || !strings.HasPrefix(strings.ToLower(p), strings.ToLower(root)+"/") {
			return "", false
		}
		p = p[len(root):]
	case strings.HasPrefix(p, "/"):
		if root == "" || !strings.HasPrefix(p, root+"/") {
			return "", false
		}
		p = p[len(root):]
	}
	clean := path.Clean(strings.TrimLeft(p, "/"))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	return clean, true
}

func hasDriveLetter(p string) bool {
	return len(p) >= 2 && p[1] == ':' && ((p[0] >= 'a' && p[0] <= 'z') || (p[0] >= 'A' && p[0] <= 'Z'))
}
