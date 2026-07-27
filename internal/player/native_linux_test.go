//go:build linux

package player

import (
	"bytes"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDetectFormat(t *testing.T) {
	for _, tc := range []struct {
		hint, contentType string
		data              []byte
		want              string
	}{
		{hint: "mp3", want: "mp3"},
		{contentType: "audio/flac", want: "flac"},
		{data: []byte("OggS"), want: "ogg"},
		{data: []byte("RIFF0000WAVE"), want: "wav"},
		{data: []byte{0xff, 0xfb}, want: "mp3"},
	} {
		if got := detectFormat(tc.hint, tc.contentType, tc.data); got != tc.want {
			t.Fatalf("detectFormat(%q, %q, %q) = %q, want %q", tc.hint, tc.contentType, tc.data, got, tc.want)
		}
	}
}

func TestUnsupportedResponseReturnsWithoutWaitingForDownloader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not audio"))
	}))
	defer server.Close()
	if _, _, err := prepareNativeTrack(server.URL, ""); err == nil {
		t.Fatal("expected unsupported format error")
	}
}

func TestGrowingWAVContinuesAfterCacheCompletes(t *testing.T) {
	const frames = 100_000
	data := make([]byte, frames*4)
	var wav bytes.Buffer
	wav.WriteString("RIFF")
	_ = binary.Write(&wav, binary.LittleEndian, uint32(36+len(data)))
	wav.WriteString("WAVEfmt ")
	_ = binary.Write(&wav, binary.LittleEndian, uint32(16))
	_ = binary.Write(&wav, binary.LittleEndian, uint16(1))
	_ = binary.Write(&wav, binary.LittleEndian, uint16(2))
	_ = binary.Write(&wav, binary.LittleEndian, uint32(44_100))
	_ = binary.Write(&wav, binary.LittleEndian, uint32(44_100*4))
	_ = binary.Write(&wav, binary.LittleEndian, uint16(4))
	_ = binary.Write(&wav, binary.LittleEndian, uint16(16))
	wav.WriteString("data")
	_ = binary.Write(&wav, binary.LittleEndian, uint32(len(data)))
	wav.Write(data)
	payload := wav.Bytes()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write(payload[:initialBytes])
		if flush, ok := w.(http.Flusher); ok {
			flush.Flush()
		}
		time.Sleep(100 * time.Millisecond)
		_, _ = w.Write(payload[initialBytes:])
	}))
	defer server.Close()
	track, _, err := prepareNativeTrack(server.URL, "wav")
	if err != nil {
		t.Fatal(err)
	}
	defer closeNativeTrack(track)
	growing := &growingStreamer{track: track}
	buffer := make([][2]float64, 1024)
	total := 0
	for {
		n, ok := growing.Stream(buffer)
		total += n
		if !ok {
			break
		}
	}
	if total != frames {
		t.Fatalf("decoded %d frames, want %d", total, frames)
	}
}
