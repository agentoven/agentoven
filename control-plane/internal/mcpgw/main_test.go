package mcpgw

import (
	"os"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/netguard"
)

// The tests call tool servers on loopback; the guard (pkg/netguard) refuses loopback unless it is
// allowlisted, and a test that exercises the guard says so itself.
func TestMain(m *testing.M) {
	os.Setenv(netguard.EnvAllow, "127.0.0.1,localhost")
	os.Exit(m.Run())
}
