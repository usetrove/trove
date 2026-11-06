package features

import (
	"fmt"
	"strings"
	"testing"
)

func BenchmarkToolResultAtTheScanCap(b *testing.B) {
	var sb strings.Builder
	for i := 0; i < MaxScanLines+2000; i++ {
		fmt.Fprintf(&sb, "--- PASS: TestCase%05d (0.00s) some more text to make the line realistic\n", i)
	}
	out := []byte(sb.String())
	b.SetBytes(int64(len(out)))
	for i := 0; i < b.N; i++ {
		ToolResult("", out)
	}
}

func BenchmarkToolResultTypical(b *testing.B) {
	out := []byte(strings.Repeat("ok  \tpkg/x\t0.123s\n", 100)) // about 2 KB, a normal command's output
	b.SetBytes(int64(len(out)))
	for i := 0; i < b.N; i++ {
		ToolResult("", out)
	}
}
