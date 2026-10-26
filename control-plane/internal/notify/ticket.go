package notify

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// A browser cannot set headers on a WebSocket, so it cannot present an API key. A backend that holds the
// key asks for a ticket instead: a short-lived token good for listening to one channel of one kitchen,
// which the page offers as the `agentoven-token.<ticket>` WebSocket subprotocol.

// TicketSubprotocolPrefix is what comes before the ticket in the subprotocol the client offers.
const TicketSubprotocolPrefix = "agentoven-token."

// TicketTTL is how long a ticket may be used to start listening. A connection that has started keeps listening.
const TicketTTL = 60 * time.Second

// MintTicket signs a ticket for listening to channel of kitchen until it expires.
func MintTicket(secret []byte, kitchen, channel string, ttl time.Duration, now time.Time) string {
	claim := strings.Join([]string{kitchen, channel, strconv.FormatInt(now.Add(ttl).Unix(), 10)}, "\x1f")
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(claim))
	return base64.RawURLEncoding.EncodeToString([]byte(claim)) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// VerifyTicket checks a ticket and returns the kitchen and channel it is for.
func VerifyTicket(secret []byte, ticket string, now time.Time) (kitchen, channel string, err error) {
	parts := strings.SplitN(ticket, ".", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("malformed ticket")
	}
	claim, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", "", fmt.Errorf("malformed ticket")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", "", fmt.Errorf("malformed ticket")
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(claim)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return "", "", fmt.Errorf("ticket not issued by this server")
	}
	fields := strings.Split(string(claim), "\x1f")
	if len(fields) != 3 {
		return "", "", fmt.Errorf("malformed ticket")
	}
	exp, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil {
		return "", "", fmt.Errorf("malformed ticket")
	}
	if now.Unix() > exp {
		return "", "", fmt.Errorf("ticket expired")
	}
	return fields[0], fields[1], nil
}
