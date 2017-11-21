package rap

var (
	// raceEnabled will be true if -race is enabled (see raceenabled.go)
	raceEnabled bool
)

// RaceEnabled will return true if -race is enabled.
func RaceEnabled() bool {
	return raceEnabled
}
