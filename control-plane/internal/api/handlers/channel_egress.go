package handlers

import (
	"context"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/agentoven/agentoven/control-plane/pkg/netguard"
)

// checkChannelURL refuses a notification channel whose URL points at the control plane's own network
// (loopback, private and link-local ranges, cloud metadata; see pkg/netguard), unless the operator or the
// kitchen allowlisted the host for channels (AGENTOVEN_CHANNEL_EGRESS_ALLOW and the kitchen's channel list;
// the tools' lists do not count). A channel's URL is chosen by whoever may create channels and called from
// inside the deployment, so it is the same request-forgery door a tool endpoint is. The check here reports the
// mistake when the channel is saved; delivery checks again at dial time, which is what holds against a name
// that changes its answer. An e-mail channel names an SMTP server in its config, and a WebSocket channel is
// listened to, not called: neither has a URL to check.
func checkChannelURL(ctx context.Context, kitchen string, kind models.ChannelKind, url string) error {
	if url == "" || kind == models.ChannelEmail || kind == models.ChannelWebSocket {
		return nil
	}
	return netguard.Channels().CheckURL(ctx, kitchen, url)
}
