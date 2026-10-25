package handlers

import (
	"os"
	"strconv"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/executor"
)

// traceStatus is the status a stored trace gets. A run that ran out of turns answered with
// what it had, but that is not the same thing as finishing, so it is recorded as it was.
func traceStatus(t *executor.ExecutionTrace) string {
	if t != nil && t.Outcome != "" {
		return t.Outcome
	}
	return "completed"
}

// envDays reads a number of days from the environment, falling back to def when it is unset
// or not a non-negative integer. 0 is a real value (it disables whatever the setting bounds).
func envDays(name string, def int) time.Duration {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return time.Duration(n) * 24 * time.Hour
		}
	}
	return time.Duration(def) * 24 * time.Hour
}

func envInt(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func osGetenv(name string) string { return os.Getenv(name) }
