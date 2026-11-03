package handlers

import (
	"os"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/netguard"
)

// The tests register and call tool servers on loopback; the guard (pkg/netguard) refuses loopback
// unless it is allowlisted. tool_security_test.go clears the variable where it tests the refusal.
func TestMain(m *testing.M) {
	os.Setenv(netguard.EnvAllow, "127.0.0.1,localhost")
	os.Exit(m.Run())
}
