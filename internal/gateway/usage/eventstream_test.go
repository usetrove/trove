package usage

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func strHeader(name, val string) []byte {
	b := []byte{byte(len(name))}
	b = append(b, name...)
	b = append(b, 7, 0, 0)
	binary.BigEndian.PutUint16(b[len(b)-2:], uint16(len(val)))
	return append(b, val...)
}

// frame builds one event stream message by hand.
func frame(headers []byte, payload []byte) []byte {
	total := preludeLen + len(headers) + len(payload) + 4
	b := make([]byte, 0, total)
	b = binary.BigEndian.AppendUint32(b, uint32(total))
	b = binary.BigEndian.AppendUint32(b, uint32(len(headers)))
	b = binary.BigEndian.AppendUint32(b, crc32.Checksum(b[:8], crcTable))
	b = append(b, headers...)
	b = append(b, payload...)
	return binary.BigEndian.AppendUint32(b, crc32.Checksum(b, crcTable))
}

func chunkFrame(inner string) []byte {
	payload, _ := json.Marshal(map[string]string{"bytes": base64.StdEncoding.EncodeToString([]byte(inner))})
	h := append(strHeader(":event-type", "chunk"), strHeader(":content-type", "application/json")...)
	h = append(h, strHeader(":message-type", "event")...)
	return frame(h, payload)
}

// streamFromSSE rebuilds the SSE fixture as an event stream: same events, other framing.
func streamFromSSE(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var out []byte
	for _, line := range strings.Split(string(raw), "\n") {
		if data, ok := strings.CutPrefix(line, "data: "); ok {
			out = append(out, chunkFrame(data)...)
		}
	}
	return out
}

func feed(x Extractor, data []byte, step int, clean bool) Result {
	for i := 0; i < len(data); i += step {
		end := min(i+step, len(data))
		x.Write(data[i:end])
	}
	return x.Finish(clean)
}

func TestBedrockStreamMatchesTheSSEReading(t *testing.T) {
	sse, err := os.ReadFile(filepath.Join("testdata", "anthropic_stream.sse"))
	if err != nil {
		t.Fatal(err)
	}
	want := feed(New(Anthropic, "/v1/messages", true), sse, len(sse), true)
	data := streamFromSSE(t, "anthropic_stream.sse")
	// Whole, byte-at-a-time and odd-sized chunks must all give the same reading.
	for _, step := range []int{len(data), 1, 7, 64} {
		got := feed(NewBedrockStream(), data, step, true)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("step %d:\n got %+v\nwant %+v", step, got, want)
		}
	}
	if want.Output != 510 || want.CacheRead != 1500 {
		t.Errorf("fixture reading changed: %+v", want)
	}
}

func TestBedrockStreamBrokenFramingIsIncompleteNotWrong(t *testing.T) {
	data := streamFromSSE(t, "anthropic_stream.sse")
	data[len(data)/2] ^= 0xff // corrupt a byte: a CRC must catch it
	got := feed(NewBedrockStream(), data, 13, true)
	if got.ErrorClass != "stream_incomplete" {
		t.Errorf("corrupt stream reported as %+v", got)
	}
	// A truncated stream (connection cut) is incomplete too.
	full := streamFromSSE(t, "anthropic_stream.sse")
	got = feed(NewBedrockStream(), full[:len(full)-30], 100, false)
	if got.ErrorClass != "stream_incomplete" {
		t.Errorf("truncated stream reported as %+v", got)
	}
	// Garbage never panics.
	feed(NewBedrockStream(), bytes.Repeat([]byte{0xff}, 200), 5, true)
	feed(NewBedrockStream(), nil, 5, true)
}

func TestBedrockStreamException(t *testing.T) {
	h := append(strHeader(":message-type", "exception"), strHeader(":exception-type", "throttlingException")...)
	data := append(streamFromSSE(t, "anthropic_stream.sse"), frame(h, []byte(`{"message":"slow down"}`))...)
	got := feed(NewBedrockStream(), data, 50, true)
	if got.ErrorClass != "stream_error:throttlingException" {
		t.Errorf("got %q", got.ErrorClass)
	}
}
