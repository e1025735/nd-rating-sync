//go:build !tinygo

package media

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The rule every extractor follows: a file is untagged only when its
// container was read to a clean end and holds no rating tag; anything else is
// unreadable, so clear_rating_if_untagged can never wipe its rating.

// riffChunk builds one RIFF chunk; size overrides the declared size when >= 0.
func riffChunk(fourCC string, body []byte, size int64) []byte {
	var out bytes.Buffer
	out.WriteString(fourCC)
	declared := uint32(len(body))
	if size >= 0 {
		declared = uint32(size)
	}
	_ = binary.Write(&out, binary.LittleEndian, declared)
	out.Write(body)
	return out.Bytes()
}

func riffWAVE(chunks ...[]byte) []byte {
	body := []byte("WAVE")
	for _, c := range chunks {
		body = append(body, c...)
	}
	return append(riffChunk("RIFF", nil, int64(len(body))), body...)
}

// asfObject builds one ASF object (GUID + 64-bit size + body).
func asfObject(guid, body []byte) []byte {
	out := append([]byte(nil), guid...)
	out = binary.LittleEndian.AppendUint64(out, uint64(24+len(body)))
	return append(out, body...)
}

// asfHeader builds an ASF header object around children; size overrides the
// declared header size when > 0.
func asfHeader(size uint64, children ...[]byte) []byte {
	var body []byte
	for _, c := range children {
		body = append(body, c...)
	}
	if size == 0 {
		size = uint64(30 + len(body))
	}
	out := append([]byte(nil), asfHeaderObjectGUID...)
	out = binary.LittleEndian.AppendUint64(out, size)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(children)))
	out = append(out, 0x01, 0x02)
	return append(out, body...)
}

var otherASFGUID = bytes.Repeat([]byte{0xAB}, 16)

func TestUntaggedOnlyAfterACleanRead(t *testing.T) {
	fmtChunk := riffChunk("fmt ", make([]byte, 16), -1)
	m4aNoMoov := append(buildAtom("ftyp", []byte("M4A \x00\x00\x00\x00")), buildAtom("mdat", junkAudio())...)
	m4aTruncatedMdat := append(buildAtom("ftyp", []byte("M4A \x00\x00\x00\x00")),
		append(binary.BigEndian.AppendUint32(nil, 1<<20), []byte("mdat")...)...) // mdat claims 1 MiB, file ends

	for _, tc := range []struct {
		name, ext string
		content   []byte
		want      fileReadResult
	}{
		// ── stays untagged ────────────────────────────────────────────────
		{"MP3 without ID3v2", "mp3", append([]byte{0xFF, 0xFB, 0x90, 0x64}, junkAudio()...), tagAbsent},
		{"FLAC whose metadata ends without comments", "flac", []byte{'f', 'L', 'a', 'C', 0x81, 0, 0, 0}, tagAbsent},
		{"WAV with only fmt and data", "wav", riffWAVE(fmtChunk, riffChunk("data", junkAudio(), -1)), tagAbsent},
		{"WAV whose odd last chunk has no pad byte", "wav", riffWAVE(fmtChunk, riffChunk("data", []byte{1, 2, 3}, -1)), tagAbsent},
		{"DSF with tag offset 0", "dsf", makeDSF(t, nil), tagAbsent},
		{"WMA header without content description", "wma", asfHeader(0, asfObject(otherASFGUID, make([]byte, 8))), tagAbsent},

		// ── now unreadable ────────────────────────────────────────────────
		{"empty MP3", "mp3", nil, fileUnreadable},
		{"FLAC with wrong magic", "flac", []byte("OggS but not flac at all"), fileUnreadable},
		{"empty FLAC", "flac", nil, fileUnreadable},
		{"RF64 WAV", "wav", append([]byte("RF64\xff\xff\xff\xffWAVE"), junkAudio()...), fileUnreadable},
		{"WAV data chunk past the end of the file", "wav", riffWAVE(fmtChunk, riffChunk("data", junkAudio(), 0xFFFFFFFF)), fileUnreadable},
		{"empty WAV", "wav", nil, fileUnreadable},
		{"DSF with wrong magic", "dsf", append([]byte("FAKE"), make([]byte, 24)...), fileUnreadable},
		{"DSF tag offset past the end", "dsf", func() []byte {
			d := makeDSF(t, nil)
			binary.LittleEndian.PutUint64(d[20:28], 1<<30)
			return d
		}(), fileUnreadable},
		{"DSF tag offset pointing at no ID3 tag", "dsf", func() []byte {
			d := append(makeDSF(t, nil), junkAudio()...)
			binary.LittleEndian.PutUint64(d[20:28], 28)
			return d
		}(), fileUnreadable},
		{"Ogg with wrong magic", "ogg", []byte("definitely not an ogg file"), fileUnreadable},
		{"Ogg-FLAC (unsupported codec)", "oga", makeOggSinglePage(t,
			append([]byte{0x7F}, []byte("FLAC\x01\x00\x00\x01fLaC")...),
			append([]byte{0x84, 0, 0, 8}, make([]byte, 8)...)), fileUnreadable},
		{"M4A without moov", "m4a", m4aNoMoov, fileUnreadable},
		{"M4A cut off inside mdat before moov", "m4a", m4aTruncatedMdat, fileUnreadable},
		{"empty M4A", "m4a", nil, fileUnreadable},
		{"ADTS .aac without ID3v2 (APEv2 is not read)", "aac", append([]byte{0xFF, 0xF1, 0x50, 0x80, 0x2E, 0x7F, 0xFC}, junkAudio()...), fileUnreadable},
		{".aac that is neither ADTS nor MP4", "aac", []byte("definitely not aac audio"), fileUnreadable},
		{"empty .aac", "aac", nil, fileUnreadable},
		{"WMA with wrong GUID", "wma", append(bytes.Repeat([]byte{0x11}, 30), junkAudio()...), fileUnreadable},
		{"WMA header larger than the file", "wma", asfHeader(1<<30, asfObject(otherASFGUID, nil)), fileUnreadable},
		{"WMA child object larger than the header", "wma", func() []byte {
			h := asfHeader(0, asfObject(otherASFGUID, make([]byte, 8)))
			binary.LittleEndian.PutUint64(h[30+16:30+24], 1000)
			return h
		}(), fileUnreadable},
		{"empty WMA", "wma", nil, fileUnreadable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeBinFile(t, t.TempDir(), "song."+tc.ext, tc.content)
			_, result := extractStarsFromFile(path, tc.ext, []string{"WMP", "iTunes", "MediaMonkey", "foobar2000", "MusicBee"})
			assert.Equal(t, tc.want, result)
		})
	}
}

// libFLAC skips an ID3v2 tag in front of the stream; so does the extractor,
// so the Vorbis comment behind it is read.
func TestExtractFLACMetadata_SkipsLeadingID3v2(t *testing.T) {
	tag := realID3v2(t, "1.0") // must NOT be read: the FLAC rating wins
	content := append(tag, makeFLAC(t, "FMPS_RATING=0.6")...)
	path := writeBinFile(t, t.TempDir(), "song.flac", content)

	stars, result := extractStarsFromFile(path, "flac", []string{"MediaMonkey"})
	assert.Equal(t, tagFound, result)
	assert.Equal(t, 3, stars)
}

// QuickTime-style files write meta as a plain atom (no version/flags).
func TestParseM4ARating_QuickTimeStyleMeta(t *testing.T) {
	ilst := buildAtom("ilst", buildFreeformAtom("com.apple.iTunes", "RATE", "80"))
	hdlr := buildAtom("hdlr", make([]byte, 25))
	meta := buildAtom("meta", append(hdlr, ilst...))
	data := buildAtom("moov", buildAtom("udta", meta))

	stars, found, err := ratingFromM4A(data, "", []string{"MusicBee"})
	assert.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, 4, stars)
}

func TestRatingFromM4A_CorruptTagListIsError(t *testing.T) {
	ilst := buildAtom("ilst", buildFreeformAtom("com.apple.iTunes", "RATE", "80"))
	binary.BigEndian.PutUint32(ilst[8:12], 0xFFFF) // first ---- atom claims more than ilst holds
	meta := buildFullAtom("meta", ilst)
	data := buildAtom("moov", buildAtom("udta", meta))

	_, found, err := ratingFromM4A(data, "", []string{"MusicBee"})
	assert.False(t, found)
	assert.Error(t, err)
}

func TestRatingFromM4A_MissingTagAtomsAreUntagged(t *testing.T) {
	for name, data := range map[string][]byte{
		"no data": nil,
		"no udta": buildAtom("moov", buildAtom("trak", nil)),
		"no meta": buildAtom("moov", buildAtom("udta", nil)),
		"no ilst": buildAtom("moov", buildAtom("udta", buildFullAtom("meta", nil))),
	} {
		_, found, err := ratingFromM4A(data, "", []string{"MusicBee"})
		assert.NoError(t, err, name)
		assert.False(t, found, name)
	}
}

func TestRatingFromWMA_DescriptorPastObjectIsError(t *testing.T) {
	data := buildWMA(t, []wmaDescriptor{{name: "WM/SharedUserRating", valueType: 3, value: wmaDWordValue(99)}})
	// Claim two descriptors while only one is present.
	binary.LittleEndian.PutUint16(data[54:56], 2)

	_, found, err := ratingFromWMA(data, "", []string{"WMP"})
	assert.False(t, found)
	assert.Error(t, err)
}

// A file made of nothing but minimal container entries is unreadable after
// maxContainerEntries, instead of keeping the callback busy for each one.
func TestExtractors_TooManyContainerEntries(t *testing.T) {
	many := maxContainerEntries + 1
	flac := []byte("fLaC")
	for i := 0; i < many; i++ {
		flac = append(flac, 1, 0, 0, 0) // empty PADDING block, not last
	}
	flac = append(flac, 0x81, 0, 0, 0)

	var wavChunks [][]byte
	for i := 0; i < many; i++ {
		wavChunks = append(wavChunks, riffChunk("junk", nil, -1))
	}

	m4a := []byte{}
	for i := 0; i < many; i++ {
		m4a = append(m4a, buildAtom("free", nil)...)
	}
	m4a = append(m4a, buildAtom("moov", nil)...)

	var moovChildren []byte
	for i := 0; i < many; i++ {
		moovChildren = append(moovChildren, buildAtom("free", nil)...)
	}
	m4aMoov := append(buildAtom("ftyp", []byte("M4A \x00\x00\x00\x00")), buildAtom("moov", moovChildren)...)

	wma := asfHeader(0)
	binary.LittleEndian.PutUint32(wma[24:28], uint32(many))

	for name, tc := range map[string]struct {
		ext     string
		content []byte
	}{
		"FLAC blocks":    {"flac", flac},
		"WAV chunks":     {"wav", riffWAVE(wavChunks...)},
		"MP4 top atoms":  {"m4a", m4a},
		"MP4 moov atoms": {"m4a", m4aMoov},
		"ASF objects":    {"wma", wma},
	} {
		path := writeBinFile(t, t.TempDir(), "song."+tc.ext, tc.content)
		_, result := extractStarsFromFile(path, tc.ext, []string{"WMP", "MediaMonkey", "MusicBee"})
		assert.Equal(t, fileUnreadable, result, name)
	}
}
