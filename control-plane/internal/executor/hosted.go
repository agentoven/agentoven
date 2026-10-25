package executor

import (
	"context"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// SetHostedInvoker installs how a delegation reaches an agent that runs in its own process (an
// agentic bot). The control plane's invoke path provides it, so a delegated run is registered and
// governed exactly like one invoked from outside. Pass nil to remove it.
func (e *Executor) SetHostedInvoker(f func(ctx context.Context, agent *models.Agent, message string) (string, error)) {
	e.hostedInvoke = f
}
