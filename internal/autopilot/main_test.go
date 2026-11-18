package autopilot

import (
	"os"
	"testing"
)

// Trial-mechanics tests plant a fixed number of sessions and expect the placeholder probation size. The shipped
// thresholds.toml changes whenever a study is published, so every test here runs on the placeholders;
// thresholds_test.go checks the shipped file and withThresholds directly.
func TestMain(m *testing.M) {
	restore := UsePlaceholderThresholdsForTest()
	code := m.Run()
	restore()
	os.Exit(code)
}
