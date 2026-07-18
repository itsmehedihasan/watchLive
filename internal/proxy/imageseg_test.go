package proxy

import (
	"bytes"
	"image"
	"image/png"
	"testing"
)

// makePNG returns a valid (tiny) PNG, exercising the real chunk structure our
// walker must traverse rather than a hand-faked one.
func makePNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

// fakeTS builds n MPEG-TS packets (188 bytes each, sync byte 0x47).
func fakeTS(n int) []byte {
	ts := make([]byte, n*188)
	for i := 0; i < n; i++ {
		ts[i*188] = tsSyncByte
	}
	return ts
}

func TestDecodeImageWrappedTS_Unwraps(t *testing.T) {
	cover := makePNG(t)
	ts := fakeTS(3)
	wrapped := append(append([]byte{}, cover...), ts...)

	got, ct := decodeImageWrappedTS(wrapped, "image/png")
	if ct != "video/MP2T" {
		t.Errorf("content type = %q, want video/MP2T", ct)
	}
	if !bytes.Equal(got, ts) {
		t.Errorf("payload mismatch: got %d bytes, want %d", len(got), len(ts))
	}
}

func TestDecodeImageWrappedTS_RealPNGPassesThrough(t *testing.T) {
	// A genuine image (no TS appended) must be returned untouched — the safety
	// gate is "does an MPEG-TS sync byte follow IEND?".
	cover := makePNG(t)
	got, ct := decodeImageWrappedTS(cover, "image/png")
	if ct != "image/png" || !bytes.Equal(got, cover) {
		t.Errorf("real PNG was modified: ct=%q len=%d", ct, len(got))
	}
}

func TestDecodeImageWrappedTS_NonPNGPassesThrough(t *testing.T) {
	ts := fakeTS(2) // bare transport stream, no PNG wrapper
	got, ct := decodeImageWrappedTS(ts, "video/MP2T")
	if ct != "video/MP2T" || !bytes.Equal(got, ts) {
		t.Errorf("non-PNG body was modified: ct=%q", ct)
	}
}

func TestDecodeImageWrappedTS_PNGFollowedByNonTS(t *testing.T) {
	// PNG with appended junk that is NOT a TS stream: must pass through unchanged
	// so we never corrupt an arbitrary image with trailing metadata.
	cover := makePNG(t)
	wrapped := append(append([]byte{}, cover...), []byte("not a transport stream")...)
	got, ct := decodeImageWrappedTS(wrapped, "image/png")
	if ct != "image/png" || !bytes.Equal(got, wrapped) {
		t.Errorf("png+junk was modified: ct=%q", ct)
	}
}

func TestPNGPayloadOffset_Truncated(t *testing.T) {
	cover := makePNG(t)
	if _, ok := pngPayloadOffset(cover[:len(cover)-6]); ok {
		t.Error("truncated PNG should report ok=false")
	}
	if _, ok := pngPayloadOffset([]byte{0x89, 'P', 'N'}); ok {
		t.Error("too-short input should report ok=false")
	}
}
