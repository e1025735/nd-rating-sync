package media

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// extractOggPackets walks an Ogg bitstream, defragments segments into packets,
// and returns up to maxPackets packets from the leading logical bitstream.
// Continued packets that span pages (or segments at the 255-byte boundary)
// are reassembled. Subsequent chained streams are not read.
//
// Per the Ogg spec (RFC 3533), each page header is 27 fixed bytes plus a
// segment table; a packet ends on the first segment with length < 255.
// The 32-bit page checksum is not verified — we only need the comment
// header, never the audio data, so a permissive walk is fine.
func extractOggPackets(data []byte, maxPackets int) ([][]byte, error) {
	var packets [][]byte
	var current []byte
	pos := 0
	for pos < len(data) && len(packets) < maxPackets {
		if pos+27 > len(data) || string(data[pos:pos+4]) != "OggS" {
			return nil, errors.New("not an Ogg stream (OggS magic missing)")
		}
		nSegs := int(data[pos+26])
		headerEnd := pos + 27 + nSegs
		if headerEnd > len(data) {
			return nil, errors.New("truncated Ogg segment table")
		}
		segTable := data[pos+27 : headerEnd]

		offset := headerEnd
		for _, segLen := range segTable {
			end := offset + int(segLen)
			if end > len(data) {
				return nil, errors.New("Ogg segment exceeds file")
			}
			current = append(current, data[offset:end]...)
			offset = end
			if segLen < 255 {
				packets = append(packets, current)
				current = nil
				if len(packets) >= maxPackets {
					break
				}
			}
		}
		pos = offset
	}
	return packets, nil
}

// parseOggVorbisRating parses an Ogg-Vorbis or Ogg-Opus stream for a star
// rating. The Ogg container's second packet is the comment header; after
// stripping the format-specific magic prefix, its body is a standard Vorbis
// comment block (vendor + count + entries).
//
// For Vorbis the comment packet starts with byte 0x03 then "vorbis"; for
// Opus it starts with "OpusTags". A trailing Vorbis "framing bit" (Vorbis
// only) is ignored — parseVorbisCommentBlock stops once it has read the
// declared comment count.
func parseOggVorbisRating(data []byte, path string, tagOrder []string) (int, bool) {
	stars, ok, _ := ratingFromOgg(data, path, tagOrder)
	return stars, ok
}

// ratingFromOgg is parseOggVorbisRating with the parse error kept. Empty data
// is untagged; a missing comment packet, an unknown codec, a broken page
// layout or a broken comment block is an error.
func ratingFromOgg(data []byte, path string, tagOrder []string) (int, bool, error) {
	if len(data) == 0 {
		return 0, false, nil
	}
	packets, err := extractOggPackets(data, 2)
	if err != nil {
		return 0, false, err
	}
	if len(packets) < 2 {
		return 0, false, errors.New("Ogg stream has no comment header packet")
	}
	commentPkt := packets[1]

	var body []byte
	switch {
	case len(commentPkt) >= 7 && commentPkt[0] == 0x03 && string(commentPkt[1:7]) == "vorbis":
		body = commentPkt[7:]
	case len(commentPkt) >= 8 && string(commentPkt[:8]) == "OpusTags":
		body = commentPkt[8:]
	default:
		return 0, false, errOggUnsupportedCodec
	}

	cmts, err := parseVorbisCommentBlock(body)
	if err != nil {
		return 0, false, err
	}
	return ratingFromVorbisComments(cmts, path, tagOrder)
}

// Bounds for extractOggMetadata. They only guard against corrupt or hostile
// files; real Vorbis/Opus headers stay far below every one of them.
const (
	// oggMaxIdentPacket bounds the identification header (packet 0), which
	// is a few dozen bytes for Vorbis and Opus.
	oggMaxIdentPacket = 64 * 1024
	// oggMaxKeptComment is the largest comment entry that is read into
	// memory. Longer entries (cover art in METADATA_BLOCK_PICTURE, lyrics…)
	// are Seeked past unread; no rating value comes close to this size.
	oggMaxKeptComment = 4 * 1024
	// oggMaxComments bounds how many comment entries are walked.
	oggMaxComments = 10000
	// oggMaxPages bounds how many pages the header walk may cross.
	oggMaxPages = 4096
)

var errOggTruncated = errors.New("Ogg stream ends inside the header packets")

// errOggUnsupportedCodec is a comment header that is neither Vorbis nor Opus
// (Ogg-FLAC, Speex, a Skeleton stream first, …). Its tags are not read, so
// the file is unreadable rather than untagged.
var errOggUnsupportedCodec = errors.New("Ogg stream is neither Vorbis nor Opus")

// oggPacketReader reads one logical packet at a time straight from the file,
// following the lacing values across page boundaries. It holds only the
// current page's lacing table; skip Seeks over packet bytes instead of
// reading them, so a multi-MiB cover-art comment never enters memory.
type oggPacketReader struct {
	f         *os.File
	lacingBuf [255]byte
	lacing    []byte // lacing values still to consume on the current page
	segLeft   int    // bytes left in the current segment
	lastSeg   bool   // the current segment is < 255 bytes: the packet ends with it
	pages     int
}

// nextPage reads the next page header and its lacing table. The page's data
// follows directly in the file, which is where the segment reads continue.
func (r *oggPacketReader) nextPage() error {
	if r.pages >= oggMaxPages {
		return fmt.Errorf("Ogg header packets span more than %d pages", oggMaxPages)
	}
	var hdr [27]byte
	if _, err := io.ReadFull(r.f, hdr[:]); err != nil {
		return oggReadErr(err)
	}
	if string(hdr[:4]) != "OggS" {
		return errors.New("Ogg page magic missing inside the header packets")
	}
	n := int(hdr[26])
	if _, err := io.ReadFull(r.f, r.lacingBuf[:n]); err != nil {
		return oggReadErr(err)
	}
	r.lacing = r.lacingBuf[:n]
	r.pages++
	return nil
}

// advance makes sure the current segment has bytes left, moving to the next
// segment (and page) as needed. It returns io.EOF once the packet's final
// segment is used up.
func (r *oggPacketReader) advance() error {
	for r.segLeft == 0 {
		if r.lastSeg {
			return io.EOF
		}
		for len(r.lacing) == 0 {
			if err := r.nextPage(); err != nil {
				return err
			}
		}
		l := r.lacing[0]
		r.lacing = r.lacing[1:]
		r.segLeft = int(l)
		r.lastSeg = l < 255
	}
	return nil
}

// startPacket begins the next packet once the previous one was read to its end.
func (r *oggPacketReader) startPacket() {
	r.lastSeg = false
}

var errOggFieldPastPacket = errors.New("Ogg header field runs past the end of its packet")

// readFull fills p from the current packet; running past the packet's end is
// an error (a header field claimed more bytes than the packet holds).
func (r *oggPacketReader) readFull(p []byte) error {
	for len(p) > 0 {
		if err := r.advance(); err != nil {
			if errors.Is(err, io.EOF) {
				return errOggFieldPastPacket
			}
			return err
		}
		n := min(len(p), r.segLeft)
		if _, err := io.ReadFull(r.f, p[:n]); err != nil {
			return oggReadErr(err)
		}
		p = p[n:]
		r.segLeft -= n
	}
	return nil
}

// skip moves n bytes forward in the current packet without reading them.
func (r *oggPacketReader) skip(n uint64) error {
	for n > 0 {
		if err := r.advance(); err != nil {
			if errors.Is(err, io.EOF) {
				return errOggFieldPastPacket
			}
			return err
		}
		k := min(n, uint64(r.segLeft))
		if _, err := r.f.Seek(int64(k), io.SeekCurrent); err != nil {
			return err
		}
		n -= k
		r.segLeft -= int(k)
	}
	return nil
}

// readPacket reads the rest of the current packet, failing if it grows past max.
func (r *oggPacketReader) readPacket(max int) ([]byte, error) {
	var out []byte
	for {
		if err := r.advance(); err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return nil, err
		}
		if len(out)+r.segLeft > max {
			return nil, fmt.Errorf("Ogg header packet exceeds %d bytes", max)
		}
		start := len(out)
		out = append(out, make([]byte, r.segLeft)...)
		if _, err := io.ReadFull(r.f, out[start:]); err != nil {
			return nil, oggReadErr(err)
		}
		r.segLeft = 0
	}
}

func (r *oggPacketReader) readUint32() (uint32, error) {
	var b [4]byte
	if err := r.readFull(b[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(b[:]), nil
}

// oggReadErr maps a short read to errOggTruncated so a cut-off file reports
// fileUnreadable (never tagAbsent, which clear_rating_if_untagged acts on).
func oggReadErr(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return errOggTruncated
	}
	return err
}

// extractOggMetadata streams the two header packets of the leading logical
// bitstream and returns a synthesised Ogg stream holding the identification
// packet plus a compact comment packet. Comment entries larger than
// oggMaxKeptComment (above all cover art, which Opus stores inside the
// comment packet as METADATA_BLOCK_PICTURE) are Seeked past without being
// read, so memory stays at a few KiB however large the art is. The synth is
// walked by parseOggVorbisRating exactly like a real file.
//
// A file that does not start with "OggS", a comment packet of an unknown
// codec, and anything cut off or malformed inside the header packets is an
// error → fileUnreadable, so such a file can never have its rating cleared.
func extractOggMetadata(f *os.File) ([]byte, error) {
	var magic [4]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil {
		return nil, oggReadErr(err)
	}
	if string(magic[:]) != "OggS" {
		return nil, errors.New("not an Ogg stream (OggS magic missing)")
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	r := &oggPacketReader{f: f}
	ident, err := r.readPacket(oggMaxIdentPacket)
	if err != nil {
		return nil, err
	}
	r.startPacket()
	comment, err := readOggCommentPacket(r)
	if err != nil {
		return nil, err
	}
	return oggSynth(ident, comment), nil
}

// readOggCommentPacket reads a Vorbis or Opus comment header from r and
// rebuilds it with an empty vendor string and only the entries worth
// keeping. A codec it does not recognise is errOggUnsupportedCodec.
func readOggCommentPacket(r *oggPacketReader) ([]byte, error) {
	var prefix [8]byte
	if err := r.readFull(prefix[:7]); err != nil {
		return nil, err
	}
	var magic []byte
	vorbis := false
	switch {
	case prefix[0] == 0x03 && string(prefix[1:7]) == "vorbis":
		magic = prefix[:7]
		vorbis = true
	case string(prefix[:7]) == "OpusTag":
		if err := r.readFull(prefix[7:8]); err != nil {
			return nil, err
		}
		if prefix[7] != 's' {
			return nil, errOggUnsupportedCodec
		}
		magic = prefix[:8]
	default:
		return nil, errOggUnsupportedCodec
	}

	vendorLen, err := r.readUint32()
	if err != nil {
		return nil, fmt.Errorf("vendor length: %w", err)
	}
	if err := r.skip(uint64(vendorLen)); err != nil {
		return nil, fmt.Errorf("vendor string: %w", err)
	}
	count, err := r.readUint32()
	if err != nil {
		return nil, fmt.Errorf("comment count: %w", err)
	}
	if count > oggMaxComments {
		count = oggMaxComments
	}

	// entries holds the kept comments in their on-disk form (u32 length +
	// "KEY=value"), ready to drop into the rebuilt packet.
	var entries []byte
	kept := uint32(0)
	for i := uint32(0); i < count; i++ {
		n, err := r.readUint32()
		if err != nil {
			return nil, fmt.Errorf("comment %d length: %w", i, err)
		}
		if n > oggMaxKeptComment {
			if err := r.skip(uint64(n)); err != nil {
				return nil, fmt.Errorf("comment %d: %w", i, err)
			}
			continue
		}
		start := len(entries)
		entries = binary.LittleEndian.AppendUint32(entries, n)
		entries = append(entries, make([]byte, n)...)
		if err := r.readFull(entries[start+4:]); err != nil {
			return nil, fmt.Errorf("comment %d: %w", i, err)
		}
		body := entries[start+4:]
		if eq := bytes.IndexByte(body, '='); eq >= 0 && strings.EqualFold(string(body[:eq]), vorbisPictureKey) {
			entries = entries[:start]
			continue
		}
		kept++
	}

	pkt := make([]byte, 0, len(magic)+8+len(entries)+1)
	pkt = append(pkt, magic...)
	pkt = binary.LittleEndian.AppendUint32(pkt, 0) // empty vendor string
	pkt = binary.LittleEndian.AppendUint32(pkt, kept)
	pkt = append(pkt, entries...)
	if vorbis {
		pkt = append(pkt, 0x01) // framing bit
	}
	return pkt, nil
}

// oggSynth lays packets out as a minimal Ogg stream: lacing values split
// across as many pages as needed (255 per page). Only what extractOggPackets
// reads is filled in; granule, serial, sequence and checksum stay zero.
func oggSynth(packets ...[]byte) []byte {
	var lacing []byte
	total := 0
	for _, p := range packets {
		n := len(p)
		for n >= 255 {
			lacing = append(lacing, 255)
			n -= 255
		}
		lacing = append(lacing, byte(n))
		total += len(p)
	}
	pages := (len(lacing) + 254) / 255
	out := make([]byte, 0, pages*27+len(lacing)+total)
	body := make([]byte, 0, total)
	for _, p := range packets {
		body = append(body, p...)
	}
	for i := 0; i < len(lacing); i += 255 {
		seg := lacing[i:min(i+255, len(lacing))]
		size := 0
		for _, l := range seg {
			size += int(l)
		}
		var hdr [27]byte
		copy(hdr[:4], "OggS")
		hdr[26] = byte(len(seg))
		out = append(out, hdr[:]...)
		out = append(out, seg...)
		out = append(out, body[:size]...)
		body = body[size:]
	}
	return out
}
