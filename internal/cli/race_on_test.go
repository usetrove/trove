//go:build race

package cli

// raceEnabled is true when the tests run under the race detector, which slows code several times over. A timing budget that
// protects real-world speed is scaled for it, so it still catches a real slowdown without failing on the detector's overhead.
const raceEnabled = true
