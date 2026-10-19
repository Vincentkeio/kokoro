package model

import "time"

// BuiltinTestTimeout is shared by Agent execution and Hub result deadlines.
// Unknown kinds have no execution permission; callers must keep their allowlist.
func BuiltinTestTimeout(kind string) time.Duration {
	switch kind {
	case "bench", "ipquality":
		return 30 * time.Minute
	case "ipquality4", "ipquality6":
		return 15 * time.Minute
	case "netquality4", "netquality6":
		return 20 * time.Minute
	case "netquality":
		return 35 * time.Minute
	default:
		return 0
	}
}
