//go:build !race

package optimize

// raceEnabled is true when the tests run under the race detector; see race_on_test.go.
const raceEnabled = false
