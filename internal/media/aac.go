package media

import (
	"bytes"
	"errors"
	"io"
	"os"
)

// mp4LeadingAtoms are the atom types an MP4/QuickTime file starts with
// (bytes 4–7). An MP4 file named .aac is read like an .m4a.
var mp4LeadingAtoms = map[string]bool{"ftyp": true, "moov": true, "mdat": true, "free": true, "skip": true, "wide": true}

// extractAACMetadata reads the tag of a .aac file, which may be either a raw
// ADTS stream or an MP4 file under the wrong extension, so the start of the
// file decides:
//
//   - "ID3": an ADTS stream with a leading ID3v2 tag, read like an MP3 tag;
//   - an MP4 atom header: the MP4 path (extractM4AMetadata);
//   - an ADTS frame header without an ID3v2 tag in front: an error. Taggers
//     such as foobar2000 store ADTS tags as APEv2 at the end of the file,
//     which is not read, so the file must not count as untagged;
//   - anything else: an error, the file is not AAC.
func extractAACMetadata(f *os.File) ([]byte, error) {
	var head [8]byte
	if _, err := io.ReadFull(f, head[:]); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, errors.New("file is too short to be AAC")
		}
		return nil, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	switch {
	case string(head[:3]) == "ID3":
		return readID3v2Tag(f, 0)
	case mp4LeadingAtoms[string(head[4:8])]:
		return extractM4AMetadata(f)
	case isADTSHeader(head[:]):
		return nil, errors.New("ADTS AAC without a leading ID3v2 tag (an APEv2 tag at the end of the file is not read)")
	default:
		return nil, errors.New("not an AAC file (neither ID3v2-tagged ADTS nor MP4)")
	}
}

// isADTSHeader reports whether b starts with an ADTS frame header: the
// 12-bit sync word 0xFFF followed by the MPEG version bit and layer 00.
func isADTSHeader(b []byte) bool {
	return len(b) >= 2 && b[0] == 0xFF && b[1]&0xF6 == 0xF0
}

// ratingFromAAC parses what extractAACMetadata returned: an ID3v2 tag, or
// a synthesised MP4 moov (which can never start with "ID3").
func ratingFromAAC(data []byte, path string, tagOrder []string) (int, bool, error) {
	if bytes.HasPrefix(data, []byte("ID3")) {
		return ratingFromID3v2(data, path, tagOrder)
	}
	return ratingFromM4A(data, path, tagOrder)
}
