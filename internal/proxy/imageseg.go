package proxy

import "encoding/binary"

// pngSignature is the 8-byte magic that opens every PNG file.
var pngSignature = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}

// tsSyncByte opens every 188-byte MPEG-TS packet.
const tsSyncByte = 0x47

// decodeImageWrappedTS unwraps an MPEG-TS segment that has been disguised as a
// PNG to ride a public image CDN (e.g. tiktokcdn.com). Some live-stream
// operators prepend a tiny valid PNG (signature + IHDR + IDAT + IEND, often a
// 1×1 pixel) to a raw MPEG-TS segment and upload the result to an image host,
// which serves it as image/png. A normal HLS client can't demux that, so we
// strip the PNG container and hand back the bare transport stream.
//
// It is deliberately conservative: it only rewrites the body when it sees a real
// PNG container immediately followed by the MPEG-TS sync byte (0x47). A genuine
// image (a channel logo proxied by mistake, say) has no TS after its IEND, so it
// passes through untouched. Returns the (possibly unchanged) body and the
// content type to serve it as.
func decodeImageWrappedTS(body []byte, contentType string) ([]byte, string) {
	off, ok := pngPayloadOffset(body)
	if !ok {
		return body, contentType
	}
	// Only unwrap if what follows the PNG is actually a transport stream. This
	// guards against mangling a legitimate PNG and against false positives.
	if off >= len(body) || body[off] != tsSyncByte {
		return body, contentType
	}
	return body[off:], "video/MP2T"
}

// pngPayloadOffset walks the PNG chunk structure from the signature to the end
// of the IEND chunk and returns the offset of the first byte after it — i.e.
// where any appended payload begins. It returns ok=false if b is not a PNG or
// the chunk stream is malformed/truncated before IEND.
//
// PNG layout: 8-byte signature, then a sequence of chunks, each
// [4-byte big-endian length][4-byte type][length bytes of data][4-byte CRC].
// IEND is the terminal chunk (zero-length data); everything after its CRC is
// not part of the image.
func pngPayloadOffset(b []byte) (int, bool) {
	if len(b) < len(pngSignature) {
		return 0, false
	}
	for i := range pngSignature {
		if b[i] != pngSignature[i] {
			return 0, false
		}
	}
	off := len(pngSignature)
	for {
		// Need at least length(4) + type(4) to read a chunk header.
		if off+8 > len(b) {
			return 0, false
		}
		length := int(binary.BigEndian.Uint32(b[off : off+4]))
		if length < 0 { // overflow guard on 32-bit ints
			return 0, false
		}
		typ := string(b[off+4 : off+8])
		// Full chunk = length(4) + type(4) + data(length) + crc(4).
		next := off + 8 + length + 4
		if next < off || next > len(b) { // overflow or truncated chunk
			return 0, false
		}
		if typ == "IEND" {
			return next, true
		}
		off = next
	}
}
