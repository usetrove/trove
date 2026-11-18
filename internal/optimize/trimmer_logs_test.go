package optimize

import (
	"fmt"
	"strings"
	"testing"
)

// appLog is a plain-text application log: many routine INFO lines, a few WARN
// and ERROR lines with a stack trace, one rare line, colored levels when color
// is set. It is not test or build output, so only log mode can trim it.
func appLog(n int, color bool) (text string, alerts []string, rare string) {
	lvl := func(l string) string {
		if color {
			return "\x1b[32m" + l + "\x1b[0m"
		}
		return l
	}
	var sb strings.Builder
	paths := []string{"orders", "users", "carts", "items"}
	for i := 0; i < n; i++ {
		ts := fmt.Sprintf("2026-11-17T10:%02d:%02d.%03dZ", i/60%60, i%60, (i*37)%1000)
		switch {
		case i == n/2:
			rare = ts + " " + lvl("INFO") + "  shutdown signal received, draining 3 connections"
			sb.WriteString(rare + "\n")
		case i%41 == 7:
			l := fmt.Sprintf("%s %s request id=%08x path=/v1/%s failed: upstream timeout", ts, lvl("ERROR"), uint32(i*2654435761), paths[i%4])
			alerts = append(alerts, l)
			sb.WriteString(l + "\n")
			sb.WriteString("java.lang.IllegalStateException: connection pool exhausted\n\tat com.shop.Pool.get(Pool.java:88)\n\tat com.shop.Api.handle(Api.java:41)\n")
		case i%53 == 11:
			l := fmt.Sprintf("%s %s slow query took=%dms table=%s", ts, lvl("WARN"), 900+i, paths[i%4])
			alerts = append(alerts, l)
			sb.WriteString(l + "\n")
		default:
			fmt.Fprintf(&sb, "%s %s  request id=%08x path=/v1/%s status=200 took=%dms\n", ts, lvl("INFO"), uint32(i*2654435761), paths[i%4], 3+i%300)
		}
	}
	return sb.String(), alerts, rare
}

// jsonLog is the same kind of log as one JSON object per line.
func jsonLog(n int) (text string, alerts []string, rare string) {
	var sb strings.Builder
	for i := 0; i < n; i++ {
		ts := fmt.Sprintf("2026-11-17T10:%02d:%02d.%03dZ", i/60%60, i%60, (i*37)%1000)
		switch {
		case i == n/2:
			rare = fmt.Sprintf(`{"ts":"%s","level":"info","msg":"config reloaded","generation":7}`, ts)
			sb.WriteString(rare + "\n")
		case i%37 == 5:
			l := fmt.Sprintf(`{"ts":"%s","level":"error","msg":"payment declined","order":%d,"code":"card_expired"}`, ts, 70000+i)
			alerts = append(alerts, l)
			sb.WriteString(l + "\n")
		default:
			fmt.Fprintf(&sb, `{"ts":"%s","level":"info","msg":"order accepted","order":%d,"latency_ms":%d}`+"\n", ts, 70000+i, 3+i%300)
		}
	}
	return sb.String(), alerts, rare
}

func TestTrimOutputTrimsLogsAndKeepsErrorsAndOutliers(t *testing.T) {
	plain, plainAlerts, plainRare := appLog(400, false)
	colored, coloredAlerts, coloredRare := appLog(400, true)
	js, jsAlerts, jsRare := jsonLog(400)
	for _, tc := range []struct {
		name   string
		in     string
		alerts []string
		rare   string
	}{
		{"plain log", plain, plainAlerts, plainRare},
		{"colored log", colored, coloredAlerts, coloredRare},
		{"json lines", js, jsAlerts, jsRare},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, changed := trimOutput(tc.in)
			if !changed || len(out) >= len(tc.in)/2 {
				t.Fatalf("expected a large reduction: %d -> %d", len(tc.in), len(out))
			}
			if len(tc.alerts) == 0 {
				t.Fatal("test log has no error lines")
			}
			for _, a := range tc.alerts {
				if !strings.Contains(out, a) {
					t.Errorf("dropped error/warning line %q", a)
				}
			}
			if !strings.Contains(out, tc.rare) {
				t.Errorf("dropped the rare line %q", tc.rare)
			}
			lines := strings.Split(strings.TrimSuffix(tc.in, "\n"), "\n")
			if !strings.HasPrefix(out, lines[0]) {
				t.Errorf("dropped the first line")
			}
			if !strings.Contains(out, lines[len(lines)-1]) {
				t.Errorf("dropped the last line")
			}
			if strings.Contains(tc.in, "IllegalStateException") && !strings.Contains(out, "at com.shop.Pool.get(Pool.java:88)") {
				t.Errorf("dropped a stack frame")
			}
			if !strings.Contains(out, "lines trimmed; full output sha256:"+recoveryID(tc.in)) {
				t.Errorf("no marker naming the full output")
			}
			// Deterministic, and trimming the trimmed text changes nothing.
			if again, _ := trimOutput(tc.in); again != out {
				t.Errorf("not deterministic")
			}
			if again, ch := trimOutput(out); ch || again != out {
				t.Errorf("trimming the trimmed log changed it")
			}
		})
	}
}

func TestTrimOutputLeavesNonLogsAlone(t *testing.T) {
	var src strings.Builder
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&src, "\tif err := step%d(); err != nil { return fmt.Errorf(\"step %d: %%w\", err) }\n", i, i)
	}
	// Every line is distinct after masking numbers, so there is nothing routine to trim.
	var uniq strings.Builder
	words := []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel", "india", "juliet",
		"kilo", "lima", "mike", "november", "oscar", "papa", "quebec", "romeo", "sierra", "tango"}
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&uniq, "2026-11-17T10:00:00Z INFO  %s %s %s happened\n", words[i%20], words[(i/20)%20], words[(i*7)%20])
	}
	for name, in := range map[string]string{"source code mentioning errors": src.String(), "log with no routine lines": uniq.String()} {
		if out, changed := trimOutput(in); changed || out != in {
			t.Errorf("%s was modified", name)
		}
	}
}
