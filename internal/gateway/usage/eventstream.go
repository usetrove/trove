package usage

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
)

// Amazon Bedrock InvokeModelWithResponseStream replies with Content-Type
// application/vnd.amazon.eventstream: a binary framing, not server-sent events.
// Verified 2026-11-04 against the API reference
// (docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_InvokeModelWithResponseStream.html):
// each "chunk" event carries a "bytes" blob, and the stream's content type is always
// application/vnd.amazon.eventstream. For the Anthropic models the decoded bytes
// are the same JSON events as the Messages stream (message_start, message_delta, ...).
//
// NOT confirmed from a fetched AWS page: the frame layout below (12-byte prelude of
// total length, headers length and a CRC-32 of those 8 bytes; headers; payload;
// CRC-32 of everything before it) is the documented event stream encoding as widely
// described, but the page I could fetch was empty. The decoder is tested against
// hand-built frames only. Check it against a real capture before relying on it
// (docs/gateway-manual-test.md, Part D).

const (
	preludeLen   = 12
	minFrameLen  = preludeLen + 4
	maxFrameSize = 24 << 20 // frames are small; refuse anything absurd rather than buffer it
)

var crcTable = crc32.MakeTable(crc32.IEEE)

// bedrockStream decodes event stream frames incrementally and feeds the Anthropic
// events inside them to the same reader the SSE path uses.
type bedrockStream struct {
	buf    []byte
	bad    bool // framing broke: stop decoding, report usage as incomplete
	inner  *anthropicStream
	sawErr string
}

func newBedrockStream() *bedrockStream { return &bedrockStream{inner: newAnthropicStream()} }

// NewBedrockStream returns the extractor for a Bedrock streamed response.
func NewBedrockStream() Extractor { return newBedrockStream() }

func (b *bedrockStream) Write(p []byte) {
	if b.bad {
		return
	}
	b.buf = append(b.buf, p...)
	for {
		n, ok := b.frame()
		if !ok {
			return
		}
		b.buf = b.buf[n:]
	}
}

// frame decodes one frame at the head of the buffer. It returns the bytes
// consumed, or ok=false when more data is needed or the framing is broken.
func (b *bedrockStream) frame() (int, bool) {
	if len(b.buf) < preludeLen {
		return 0, false
	}
	total := int(binary.BigEndian.Uint32(b.buf[0:4]))
	hlen := int(binary.BigEndian.Uint32(b.buf[4:8]))
	if crc32.Checksum(b.buf[0:8], crcTable) != binary.BigEndian.Uint32(b.buf[8:12]) ||
		total < minFrameLen || total > maxFrameSize || hlen > total-minFrameLen {
		b.bad = true
		b.buf = nil
		return 0, false
	}
	if len(b.buf) < total {
		return 0, false
	}
	msg := b.buf[:total]
	if crc32.Checksum(msg[:total-4], crcTable) != binary.BigEndian.Uint32(msg[total-4:]) {
		b.bad = true
		b.buf = nil
		return 0, false
	}
	headers := parseEventHeaders(msg[preludeLen : preludeLen+hlen])
	payload := msg[preludeLen+hlen : total-4]
	switch headers[":message-type"] {
	case "event":
		if headers[":event-type"] == "chunk" {
			b.chunk(payload)
		}
	case "exception":
		if b.sawErr == "" {
			b.sawErr = "stream_error:" + sanitize(headers[":exception-type"])
		}
	}
	return total, true
}

// chunk unwraps {"bytes": "<base64>"} and hands the inner JSON event on.
func (b *bedrockStream) chunk(payload []byte) {
	var w struct {
		Bytes string `json:"bytes"`
	}
	if json.Unmarshal(payload, &w) != nil {
		return
	}
	raw, err := base64.StdEncoding.DecodeString(w.Bytes)
	if err != nil {
		return
	}
	b.inner.event(Event{Data: raw})
}

func (b *bedrockStream) Finish(cleanEnd bool) Result {
	r := b.inner.Finish(cleanEnd && !b.bad)
	if b.sawErr != "" {
		r.ErrorClass = b.sawErr
	}
	return r
}

// parseEventHeaders reads string-typed headers (type 7) and skips the rest. It
// stops at the first thing it cannot read; the headers it needs are all strings.
func parseEventHeaders(h []byte) map[string]string {
	out := map[string]string{}
	for i := 0; i < len(h); {
		nameLen := int(h[i])
		i++
		if i+nameLen+1 > len(h) {
			return out
		}
		name := string(h[i : i+nameLen])
		i += nameLen
		typ := h[i]
		i++
		var size int
		switch typ {
		case 0, 1:
			size = 0
		case 2:
			size = 1
		case 3:
			size = 2
		case 4:
			size = 4
		case 5, 8:
			size = 8
		case 9:
			size = 16
		case 6, 7:
			if i+2 > len(h) {
				return out
			}
			size = int(binary.BigEndian.Uint16(h[i:]))
			i += 2
			if i+size > len(h) {
				return out
			}
			if typ == 7 {
				out[name] = string(h[i : i+size])
			}
		default:
			return out
		}
		i += size
	}
	return out
}

// sanitize keeps an exception name safe to store as an error class.
func sanitize(s string) string {
	if len(s) > 32 {
		s = s[:32]
	}
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' {
			out = append(out, c)
		} else {
			out = append(out, '_')
		}
	}
	return string(out)
}
