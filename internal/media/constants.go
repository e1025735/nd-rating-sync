package media

import (
	"errors"
	"fmt"
	"io"
)

// maxMetadataReadBytes is the per-format upper bound on bytes the metadata
// extractors will pull into memory from any single file. Each format reads
// only its header + tag-bearing region (using Seek to jump past audio data),
// so the actual size is normally a few KiB; the cap exists as a safety net
// against pathological/hostile files (huge embedded artwork, /dev/zero,
// corrupt-but-readable size fields).
//
// 16 MiB covers legitimate cases like multi-MiB embedded cover art in FLAC
// PICTURE blocks or MP4 udta atoms. A file whose metadata genuinely exceeds
// this is reported as fileUnreadable so clear_rating_if_untagged cannot
// wipe a rating for a file we did not fully read.
const maxMetadataReadBytes = 16 * 1024 * 1024

// maxContainerEntries bounds how many container entries one extractor walk
// may visit: FLAC metadata blocks, WAV chunks, top-level MP4 atoms, moov
// children and ASF header objects. Each entry costs a Seek and a small read
// (WASI system calls), so a corrupt or crafted file made of minimal entries
// could otherwise keep one callback busy past Navidrome's 30 s limit. Real
// files use at most a few dozen; more is reported as unreadable.
const maxContainerEntries = 4096

// isShortRead reports whether err means the file ended before a read was
// complete.
func isShortRead(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

// cutOff returns short when err is a short read and err otherwise. A file
// that ends inside its container or tag region is cut off, so the extractors
// report it with a clear error: the file is unreadable, never untagged.
func cutOff(err, short error) error {
	if isShortRead(err) {
		return short
	}
	return err
}

// errTooManyEntries is the error for a walk that reaches maxContainerEntries.
var errTooManyEntries = fmt.Errorf("file has more than %d container entries", maxContainerEntries)
