package scanner

import (
	"fmt"
	"os"
	"time"

	kvadapter "github.com/e1025735/nd-rating-sync/internal/adapter/kv_store"
	"github.com/e1025735/nd-rating-sync/internal/logging"
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
}

// watchRead returns the hook processSong calls during a crash hunt before it
// accesses the file of song songID: it stores cur (the song's own position)
// together with the song in the sweep record. If the plugin stops while the
// file is accessed, the record names it.
func watchRead(cur SyncCursor, songID string) func(path string) {
	return func(path string) {
		cur.Reading = &readingSong{ID: songID, Path: path}
		kvadapter.MarkSweepActive(cur.marshal())
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
	if info, err := os.Stat(path); err == nil && (info.ModTime().UnixNano() != f.MTime || info.Size() != f.Size) {
		logging.Info(fmt.Sprintf("nd-rating-sync: %q changed since it crashed the plugin – reading it again", path))
		kvadapter.DeleteCrashedFile(songID)
		delete(q.files, songID)
		return false
	}
	return true
}
