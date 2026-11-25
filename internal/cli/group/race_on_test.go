//go:build race

package group

// raceEnabled reports that the race detector is on, which makes the hot loops
// of the 15 KiB-per-host memory test far too slow to measure.
const raceEnabled = true
