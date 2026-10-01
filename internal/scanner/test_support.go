package scanner

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"testing"

	kvadapter "github.com/e1025735/nd-rating-sync/internal/adapter/kv_store"
	subsonicadapter "github.com/e1025735/nd-rating-sync/internal/adapter/subsonic"
	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

var kvKeyConfigHash = kvadapter.KVKeyConfigHash
var sweepStaleAfter = kvadapter.SweepStaleAfter

func kvKeySweepActive() string { return kvadapter.KVKeySweepActive() }

func resetKVStoreMock(t *testing.T) {
	t.Helper()
	host.KVStoreMock.ExpectedCalls = nil
	host.KVStoreMock.Calls = nil
	t.Cleanup(func() {
		host.KVStoreMock.ExpectedCalls = nil
		host.KVStoreMock.Calls = nil
	})
}

func mustJSONBytes(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("marshal failed: %v", err))
	}
	return b
}

func configHashFor(cfg PluginConfig) string {
	return kvadapter.ConfigHashFor(mustJSONBytes(cfg))
}

func kvKeyLastSynced(libraryID, username string) string {
	return kvadapter.KVKeyLastSynced(libraryID, username)
}

func bucketKey(libraryID string, size int64, ext string) string {
	return kvadapter.BucketKey(libraryID, size, ext)
}

func libraryScanStateKey(libraryID string) string {
	return kvadapter.LibraryScanStateKey(libraryID)
}

func subsonicOK(songs []subsonicSong) string {
	type inner struct {
		Status        string                         `json:"status"`
		SearchResult3 *subsonicadapter.SearchResult3 `json:"searchResult3,omitempty"`
	}
	type outer struct {
		Response inner `json:"subsonic-response"`
	}
	body, _ := json.Marshal(outer{
		Response: inner{Status: "ok", SearchResult3: &subsonicadapter.SearchResult3{Song: songs}},
	})
	return string(body)
}

func makeFLAC(t *testing.T, comments ...string) []byte {
	t.Helper()
	var body bytes.Buffer
	const vendor = "test"
	require.NoError(t, binary.Write(&body, binary.LittleEndian, uint32(len(vendor))))
	body.WriteString(vendor)
	require.NoError(t, binary.Write(&body, binary.LittleEndian, uint32(len(comments))))
	for _, c := range comments {
		require.NoError(t, binary.Write(&body, binary.LittleEndian, uint32(len(c))))
		body.WriteString(c)
	}

	var out bytes.Buffer
	out.WriteString("fLaC")
	blockLen := body.Len()
	out.WriteByte(0x80 | 4)
	out.WriteByte(byte(blockLen >> 16))
	out.WriteByte(byte(blockLen >> 8))
	out.WriteByte(byte(blockLen))
	out.Write(body.Bytes())
	return out.Bytes()
}

func makeOggPage(t *testing.T, isFirst bool, segments [][]byte) []byte {
	t.Helper()
	if len(segments) > 255 {
		t.Fatalf("page can hold at most 255 segments, got %d", len(segments))
	}
	var buf bytes.Buffer
	buf.WriteString("OggS")
	buf.WriteByte(0)
	var flags byte
	if isFirst {
		flags |= 0x02
	}
	buf.WriteByte(flags)
	require.NoError(t, binary.Write(&buf, binary.LittleEndian, uint64(0)))
	require.NoError(t, binary.Write(&buf, binary.LittleEndian, uint32(0)))
	require.NoError(t, binary.Write(&buf, binary.LittleEndian, uint32(0)))
	require.NoError(t, binary.Write(&buf, binary.LittleEndian, uint32(0)))
	buf.WriteByte(byte(len(segments)))
	for _, s := range segments {
		if len(s) > 255 {
			t.Fatalf("segment exceeds 255 bytes: %d", len(s))
		}
		buf.WriteByte(byte(len(s)))
	}
	for _, s := range segments {
		buf.Write(s)
	}
	return buf.Bytes()
}

func packetToSegments(pkt []byte) [][]byte {
	var segs [][]byte
	for len(pkt) >= 255 {
		segs = append(segs, append([]byte(nil), pkt[:255]...))
		pkt = pkt[255:]
	}
	segs = append(segs, append([]byte(nil), pkt...))
	return segs
}

func makeOggSinglePage(t *testing.T, packets ...[]byte) []byte {
	t.Helper()
	var allSegs [][]byte
	for _, p := range packets {
		allSegs = append(allSegs, packetToSegments(p)...)
	}
	return makeOggPage(t, true, allSegs)
}

func makeOggMultiPage(t *testing.T, segsPerPage int, packets ...[]byte) []byte {
	t.Helper()
	var allSegs [][]byte
	for _, p := range packets {
		allSegs = append(allSegs, packetToSegments(p)...)
	}
	var out bytes.Buffer
	for i := 0; i < len(allSegs); i += segsPerPage {
		end := i + segsPerPage
		if end > len(allSegs) {
			end = len(allSegs)
		}
		out.Write(makeOggPage(t, i == 0, allSegs[i:end]))
	}
	return out.Bytes()
}

func writeVorbisCommentBlock(t *testing.T, buf *bytes.Buffer, comments []string) {
	t.Helper()
	const vendor = "test"
	require.NoError(t, binary.Write(buf, binary.LittleEndian, uint32(len(vendor))))
	buf.WriteString(vendor)
	require.NoError(t, binary.Write(buf, binary.LittleEndian, uint32(len(comments))))
	for _, c := range comments {
		require.NoError(t, binary.Write(buf, binary.LittleEndian, uint32(len(c))))
		buf.WriteString(c)
	}
}

var idHeaderPlaceholder = []byte("ID-PACKET-0")

func makeVorbisCommentPacket(t *testing.T, comments ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.WriteByte(0x03)
	buf.WriteString("vorbis")
	writeVorbisCommentBlock(t, &buf, comments)
	buf.WriteByte(0x01)
	return buf.Bytes()
}

func makeOpusCommentPacket(t *testing.T, comments ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.WriteString("OpusTags")
	writeVorbisCommentBlock(t, &buf, comments)
	return buf.Bytes()
}

func makeFLACWithPadding(t *testing.T, comments ...string) []byte {
	t.Helper()
	var padded bytes.Buffer
	padded.WriteString("fLaC")
	padded.WriteByte(0x01)
	padded.WriteByte(0x00)
	padded.WriteByte(0x00)
	padded.WriteByte(0x08)
	padded.Write(make([]byte, 8))
	tail := makeFLAC(t, comments...)
	padded.Write(tail[4:])
	return padded.Bytes()
}

func resetSubsonicMock(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		host.SubsonicAPIMock.ExpectedCalls = nil
		host.SubsonicAPIMock.Calls = nil
	})
}

func resetSchedulerMock(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		host.SchedulerMock.ExpectedCalls = nil
		host.SchedulerMock.Calls = nil
	})
}

func resetLibraryMock(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		host.LibraryMock.ExpectedCalls = nil
		host.LibraryMock.Calls = nil
	})
}

func resetKVStoreMockNoDefault(t *testing.T) {
	t.Helper()
	host.KVStoreMock.ExpectedCalls = nil
	host.KVStoreMock.Calls = nil
	t.Cleanup(func() {
		host.KVStoreMock.ExpectedCalls = nil
		host.KVStoreMock.Calls = nil
	})
}

func init() {
	_ = kvadapter.KVKeyConfigHash
	_ = mock.Anything
}

func makeOggSinglePageWithTime(t *testing.T, packets ...[]byte) []byte {
	return makeOggSinglePage(t, packets...)
}
