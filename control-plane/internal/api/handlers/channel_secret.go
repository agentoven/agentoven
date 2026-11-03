package handlers

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// A channel's signing secret goes in and never comes out: the API takes "secret" on create and update, and every
// answer says only "has_secret" (and, where the store keeps secrets as credentials, the credential's name).

// shownChannel is the channel as the API returns it.
func shownChannel(ch models.NotificationChannel) models.NotificationChannel {
	ch.HasSecret = ch.Secret != "" || ch.SecretRef != ""
	ch.Secret = ""
	return ch
}

func shownChannels(in []models.NotificationChannel) []models.NotificationChannel {
	out := make([]models.NotificationChannel, len(in))
	for i, ch := range in {
		out[i] = shownChannel(ch)
	}
	return out
}

// secretUpdate is what an update body says about the signing secret: leave it (the key is absent), replace
// it, or remove it (the key is there and empty). Without this an update that does not mention the secret,
// which is every dashboard edit, would erase it.
type secretUpdate int

const (
	secretKeep secretUpdate = iota
	secretReplace
	secretRemove
)

var errServerSetRef = errors.New("secret_ref is set by the server: send the secret in \"secret\"")

// decodeChannelUpdate reads an update body: the channel it describes and what to do with its secret.
func decodeChannelUpdate(body io.Reader) (models.NotificationChannel, secretUpdate, error) {
	raw, err := io.ReadAll(io.LimitReader(body, 1<<20))
	if err != nil {
		return models.NotificationChannel{}, secretKeep, err
	}
	var ch models.NotificationChannel
	if err := json.Unmarshal(raw, &ch); err != nil {
		return ch, secretKeep, err
	}
	if ch.SecretRef != "" {
		return ch, secretKeep, errServerSetRef
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	_, said := fields["secret"]
	switch {
	case ch.Secret != "":
		return ch, secretReplace, nil
	case said:
		return ch, secretRemove, nil
	}
	return ch, secretKeep, nil
}

func respondChannelBodyError(w http.ResponseWriter, err error) {
	if errors.Is(err, errServerSetRef) {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	respondError(w, http.StatusBadRequest, "Invalid request body")
}
