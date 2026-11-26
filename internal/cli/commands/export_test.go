package commands

import "time"

// SetHostsAddPoll shortens the wait loop of `hosts add` for a test and
// returns a function that restores it.
func SetHostsAddPoll(interval time.Duration) func() {
	previous := hostsAddPoll
	hostsAddPoll = interval

	return func() { hostsAddPoll = previous }
}
