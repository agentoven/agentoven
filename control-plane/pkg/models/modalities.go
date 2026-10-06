package models

import (
	"fmt"
	"sort"
	"strings"
)

// Modalities are what an agent can take in and give out beyond text. They are
// a property of the agent's model provider — the agent has exactly what its
// provider offers — and are tuned in the provider's config:
//
//	"config": {
//	  "modalities": {
//	    "image":    {"enabled": true},
//	    "pdf":      {"enabled": false},
//	    "audio":    {"stt_model": "whisper-1", "tts_model": "tts-1"},
//	    "realtime": {"model": "gpt-realtime"}
//	  }
//	}
//
// Nothing needs to be set: what the provider's driver and the model catalog
// know is the default. An entry's "enabled" narrows that (false) or, for
// image/pdf/video on a gateway the catalog cannot know, widens it (true).
// audio and realtime need driver support, so true cannot add what the driver
// lacks. Text is always on and has no entry.
const (
	ModalityText     = "text"
	ModalityImage    = "image"
	ModalityPDF      = "pdf"
	ModalityVideo    = "video"
	ModalityAudio    = "audio"    // cascaded voice: speech-to-text in, text-to-speech out
	ModalityRealtime = "realtime" // live speech-to-speech session
)

// AllModalities is the display order of every modality.
var AllModalities = []string{ModalityText, ModalityImage, ModalityPDF, ModalityVideo, ModalityAudio, ModalityRealtime}

// modalitySettings lists the keys each modality accepts. "enabled" is a bool;
// the rest are strings.
var modalitySettings = map[string][]string{
	ModalityImage:    {"enabled"},
	ModalityPDF:      {"enabled"},
	ModalityVideo:    {"enabled"},
	ModalityAudio:    {"enabled", "stt_model", "tts_model"},
	ModalityRealtime: {"enabled", "model"},
}

func (p *ModelProvider) modalityEntry(name string) map[string]interface{} {
	all, _ := p.Config["modalities"].(map[string]interface{})
	entry, _ := all[name].(map[string]interface{})
	return entry
}

// ModalityConfigured reports whether the provider config has an entry for the
// modality at all.
func (p *ModelProvider) ModalityConfigured(name string) bool {
	return p.modalityEntry(name) != nil
}

// ModalityEnabled reports the provider's explicit on/off for a modality; set is
// false when the config says nothing.
func (p *ModelProvider) ModalityEnabled(name string) (enabled, set bool) {
	enabled, set = p.modalityEntry(name)["enabled"].(bool)
	return enabled, set
}

// ModalitySetting returns a string setting of a modality ("" if unset), e.g.
// ModalitySetting("audio", "stt_model").
func (p *ModelProvider) ModalitySetting(name, key string) string {
	s, _ := p.modalityEntry(name)[key].(string)
	return s
}

// ValidateModalities checks the "modalities" section of a provider config:
// known modalities, known keys, right types. A typo is refused here rather
// than silently ignored, which would leave a modality quietly on or off.
func ValidateModalities(config map[string]interface{}) error {
	raw, ok := config["modalities"]
	if !ok {
		return nil
	}
	all, ok := raw.(map[string]interface{})
	if !ok {
		return fmt.Errorf("config.modalities must be an object")
	}
	names := make([]string, 0, len(all))
	for name := range all {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		allowed, known := modalitySettings[name]
		if !known {
			hint := "text is always on and takes no entry; "
			if name != ModalityText {
				hint = ""
			}
			return fmt.Errorf("config.modalities.%s: %sunknown modality (use image, pdf, video, audio, realtime)", name, hint)
		}
		entry, ok := all[name].(map[string]interface{})
		if !ok && all[name] == nil {
			continue // null removes the entry on update
		}
		if !ok {
			return fmt.Errorf("config.modalities.%s must be an object", name)
		}
		for key, v := range entry {
			if v == nil {
				continue // null clears a setting on update
			}
			if !contains(allowed, key) {
				return fmt.Errorf("config.modalities.%s.%s: unknown setting (allowed: %s)", name, key, strings.Join(allowed, ", "))
			}
			if key == "enabled" {
				if _, ok := v.(bool); !ok {
					return fmt.Errorf("config.modalities.%s.enabled must be true or false", name)
				}
			} else if _, ok := v.(string); !ok {
				return fmt.Errorf("config.modalities.%s.%s must be a string", name, key)
			}
		}
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// MergeModalities applies an update to a provider's stored modalities config
// one modality at a time, so changing the audio models leaves a pdf setting
// alone. Within a modality, keys are merged and a null value deletes the key;
// an entry left empty is dropped, which puts that modality back on its default.
// Anything that is not an object is returned as sent, for validation to refuse.
func MergeModalities(existing, update interface{}) interface{} {
	upd, ok := update.(map[string]interface{})
	if !ok {
		return update
	}
	old, _ := existing.(map[string]interface{})
	out := map[string]interface{}{}
	for name, entry := range old {
		out[name] = entry
	}
	for name, entry := range upd {
		e, ok := entry.(map[string]interface{})
		if !ok {
			if entry == nil {
				delete(out, name)
				continue
			}
			out[name] = entry
			continue
		}
		merged := map[string]interface{}{}
		if prev, ok := out[name].(map[string]interface{}); ok {
			for k, v := range prev {
				merged[k] = v
			}
		}
		for k, v := range e {
			if v == nil {
				delete(merged, k)
			} else {
				merged[k] = v
			}
		}
		if len(merged) == 0 {
			delete(out, name)
		} else {
			out[name] = merged
		}
	}
	return out
}
