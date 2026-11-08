// stubagent stands in for the Claude Code CLI in QA. It never calls a model.
// It prints a version for --version, and for any other call it appends its
// arguments to stubagent-args.log beside the executable and prints a JSON reply
// with zero cost. Use it to check exactly which flags Trove passes to the agent,
// such as --max-budget-usd, without spending anything.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println("2.1.319 (Claude Code)")
		return
	}
	// The log sits next to this executable, so it does not depend on env vars
	// reaching the child process.
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), "stubagent-args.log")
		if f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			_, _ = f.WriteString(strings.Join(os.Args[1:], "\x1f") + "\n")
			_ = f.Close()
		}
	}
	reply := map[string]any{"type": "result", "result": "fake reply, no model called", "total_cost_usd": 0.0, "is_error": false}
	b, _ := json.Marshal(reply)
	fmt.Println(string(b))
}
