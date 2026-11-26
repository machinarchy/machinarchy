package commands

import "time"

// SetHostsAddPoll shortens the wait loop of `hosts add` for a test and
// returns a function that restores it.
func SetHostsAddPoll(interval time.Duration) func() {
	previous := hostsAddPoll
	hostsAddPoll = interval

	return func() { hostsAddPoll = previous }
}

// SetRunPoll shortens how often a watched run is asked for its state and
// returns a function that restores it.
func SetRunPoll(interval time.Duration) func() {
	previous := pollInterval
	pollInterval = interval

	return func() { pollInterval = previous }
}
