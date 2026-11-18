//go:build race

package optimize

// raceEnabled is true when the tests run under the race detector, which slows code several times over,
// so a wall-clock budget means nothing there.
const raceEnabled = true
