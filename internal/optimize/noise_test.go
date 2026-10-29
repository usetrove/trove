package optimize

import (
	"strings"
	"testing"
)

func noisyOutput() string {
	line := strings.Repeat("retrying connection to the build cache ", 2)
	return "\x1b[31mERROR\x1b[0m start\n" + strings.Repeat(line+"\n", 37) + "done\n"
}

func TestNoiseNormalizerProperties(t *testing.T) {
	noisy := noisyOutput()
	applies := [][]byte{
		req{msgs: []msg{user(text("go")), assistant(toolUse("t1", "bash")), user(toolResult("t1", noisy))}}.body(),
		req{msgs: []msg{user(toolResultBlocks("t1", noisy, "second block \x1b[1mbold\x1b[0m"))}}.body(),
		req{system: "sys", tools: []string{"bash"}, msgs: []msg{user(toolResult("t1", "10%\r50%\rdone"))}}.body(),
	}
	noops := [][]byte{
		req{msgs: []msg{user(text(noisy))}}.body(), // noise outside tool results is not touched
		req{msgs: []msg{user(toolResult("t1", "clean output\nline two"))}}.body(),
		req{system: noisy, msgs: []msg{user(text("hi"))}}.body(),
		req{msgs: nil}.body(),
	}
	checkProperties(t, NoiseNormalizer{}, Session{}, applies, noops)
}

func TestNoiseNormalizerOutput(t *testing.T) {
	body := req{msgs: []msg{user(toolResult("t1", noisyOutput()))}}.body()
	r, _ := ParseRequest(body)
	out, _ := NoiseNormalizer{}.Transform(t.Context(), r)
	got, _ := out.DecodeString(out.Messages[0].Blocks[0].Texts[0])
	want := "ERROR start\nretrying connection to the build cache retrying connection to the build cache \n(repeated 37 times)\ndone\n"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestNoiseNormalizerLeavesOtherBytesAlone(t *testing.T) {
	// Only the tool-result string may change; keys, order and text blocks stay.
	body := req{system: "keep me", tools: []string{"bash"}, msgs: []msg{
		user(text("hello \x1b[0m not tool output")),
		user(toolResult("t1", "\x1b[0mx")),
	}}.body()
	r, _ := ParseRequest(body)
	out, _ := NoiseNormalizer{}.Transform(t.Context(), r)
	want := strings.Replace(string(body), `\u001b[0mx`, "x", 1)
	if string(out.Raw) != want {
		t.Errorf("unexpected rewrite\n got %s\nwant %s", out.Raw, want)
	}
}
