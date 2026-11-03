package scanner

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"time"

	kvadapter "github.com/e1025735/nd-rating-sync/internal/adapter/kv_store"
	"github.com/e1025735/nd-rating-sync/internal/logging"
	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/types"
)

// A file that crashes the plugin (a panic, running out of memory, Navidrome's
// 30 s limit) would stop every sweep at the same place. After a crash the
// sweep watches every file of the crash page (watchRead); when the plugin
// stops again while it accesses one of them, startOrResumeSweep quarantines
// that file. A quarantined file is skipped for every user of its library
// until it changes.

// songEnv is what processSong consults besides the song itself.
type songEnv struct {
	quarantine *quarantine       // files that crashed the plugin; nil holds none
	watch      func(path string) // set during a crash hunt (watchRead); nil otherwise
	crashed    bool              // the song is SyncCursor.Skip: its file was just quarantined
}

// watchRead returns the hook processSong calls during a crash hunt before it
// accesses the file of song songID: it stores cur (the song's own position)
// together with the song in the sweep record. If the plugin stops while the
// file is accessed, the record names it.
func watchRead(cur SyncCursor, songID string) func(path string) {
	return func(path string) {
		cur.Reading = &readingSong{ID: songID, Path: path}
		if !kvadapter.MarkSweepActive(cur.marshal()) {
			// Without the record the file cannot be quarantined; the log is
			// then the only place that can name it.
			logging.Warn(fmt.Sprintf(
				"nd-rating-sync: reading %q (song id=%q) after an unexpected stop – it cannot be quarantined; if the plugin stops now, this is the file that crashes it",
				path, songID))
		}
	}
}

// quarantineFile stores the file that stopped the plugin in the quarantine,
// with its current size and mtime so a later change releases it.
func quarantineFile(r readingSong) {
	f := kvadapter.CrashedFile{Path: r.Path, At: time.Now().UTC()}
	if info, err := os.Stat(r.Path); err == nil {
		f.MTime, f.Size = info.ModTime().UnixNano(), info.Size()
	}
	kvadapter.SaveCrashedFile(r.ID, f)
}

// quarantine holds the quarantined files by song ID, read from the KV store
// once per callback (runSyncChunk loads it before the first pair it
// processes).
type quarantine struct {
	files map[string]kvadapter.CrashedFile
}

func loadQuarantine() *quarantine {
	return &quarantine{files: kvadapter.LoadCrashedFiles()}
}

// holds reports whether the file of song songID at path is quarantined. A
// file whose size or mtime changed since it crashed the plugin is released:
// its entry is deleted and it is read again. A quarantined file that cannot
// be stat'ed stays quarantined.
func (q *quarantine) holds(songID, path string) bool {
	if q == nil {
		return false
	}
	f, ok := q.files[songID]
	if !ok {
		return false
	}
	if changedSince(f, path) {
		logging.Info(fmt.Sprintf("nd-rating-sync: %q changed since it crashed the plugin – reading it again", path))
		kvadapter.DeleteCrashedFile(songID)
		delete(q.files, songID)
		return false
	}
	return true
}

// changedSince reports whether the file at path differs in size or mtime
// from when it was quarantined as f. A file that cannot be stat'ed counts as
// unchanged, so it stays quarantined.
func changedSince(f kvadapter.CrashedFile, path string) bool {
	info, err := os.Stat(path)
	return err == nil && (info.ModTime().UnixNano() != f.MTime || info.Size() != f.Size)
}

// pruneQuarantine deletes, once a sweep completes, the quarantine entries no
// sweep needs any more: a song Navidrome no longer has under that ID
// (deleted, or re-imported under a new one), a song whose library is no
// longer synced (removed from the config, or left without users), and a song
// whose file changed since it crashed the plugin. The last one is released
// here as well as in holds, because holds is never reached for a song every
// user has already rated (skip_already_rated). One matcher call looks all of
// them up; the file is stat'ed through its library's mount. A song Navidrome
// only marks missing keeps its entry, since it can come back unchanged. A
// failed lookup deletes nothing.
func pruneQuarantine(cfg PluginConfig) {
	files := kvadapter.LoadCrashedFiles()
	if len(files) == 0 {
		return
	}
	synced := map[int32]bool{}
	for _, lib := range cfg.Libraries {
		id, err := parseLibraryID(lib.LibraryID)
		if err != nil {
			continue
		}
		for _, u := range lib.Users {
			if u.Username != "" {
				synced[id] = true
			}
		}
	}

	ids := make([]string, 0, len(files))
	for id := range files {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	refs := make([]types.SongRef, len(ids))
	for i, id := range ids {
		refs[i] = types.SongRef{ID: id}
	}
	tracks, err := host.MatcherMatchSongs(refs, host.MatchOptions{})
	if err != nil {
		logging.Warn(fmt.Sprintf("nd-rating-sync: looking up the quarantined songs failed: %q – keeping them all", err.Error()))
		return
	}
	lookups := newHostLookups()
	for i, id := range ids {
		var t *types.Track
		if i < len(tracks) {
			t = tracks[i]
		}
		switch {
		case t == nil || t.ID != id:
			logging.Info(fmt.Sprintf("nd-rating-sync: removing %q from the quarantine – Navidrome no longer has song id=%q", files[id].Path, id))
		case !synced[t.LibraryID]:
			logging.Info(fmt.Sprintf("nd-rating-sync: removing %q from the quarantine – library %d is no longer synced", files[id].Path, t.LibraryID))
		case fileChanged(lookups, id, t, files[id]):
			logging.Info(fmt.Sprintf("nd-rating-sync: removing %q from the quarantine – the file changed since it crashed the plugin", files[id].Path))
		default:
			continue
		}
		kvadapter.DeleteCrashedFile(id)
	}
}

// fileChanged reports whether the quarantined file of track t changed since
// it was quarantined as f. A track whose file cannot be located (missing,
// no mount) or stat'ed counts as unchanged.
func fileChanged(lookups *hostLookups, songID string, t *types.Track, f kvadapter.CrashedFile) bool {
	mount, err := lookups.mount(strconv.Itoa(int(t.LibraryID)))
	if err != nil {
		return false
	}
	file := locateTrack(mount, songID, t)
	return file.found && changedSince(f, file.path)
}
