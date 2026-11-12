//go:build race

package stress

// raceEnabled is true when the tests run under the race detector, which slows this suite by roughly ten times.
const raceEnabled = true
