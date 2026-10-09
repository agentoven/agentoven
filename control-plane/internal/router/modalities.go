package router

import (
	"fmt"

	"github.com/agentoven/agentoven/control-plane/pkg/audio"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/agentoven/agentoven/control-plane/pkg/realtime"
)

// Modalities are decided per provider (see models.Modalities): the driver says
// what it can do, the catalog what the model can do, and the provider's own
// config has the last word. An agent has exactly the modalities of the
// provider it runs on — nothing is borrowed from other providers.

// RealtimeFor returns the live-session driver for a provider, or nil when its
// driver has no realtime support or the provider has the modality switched off.
// Public so Pro, which cannot name the internal driver types, can reach it
// through the pkg/realtime interface.
func (mr *ModelRouter) RealtimeFor(p *models.ModelProvider) realtime.Driver {
	if off(p, models.ModalityRealtime) {
		return nil
	}
	d, _ := mr.GetDriver(p.Kind).(realtime.Driver)
	return d
}

// SpeechFor returns the speech driver (speech-to-text and text-to-speech) for a
// provider, or nil when its driver has none or the audio modality is off.
func (mr *ModelRouter) SpeechFor(p *models.ModelProvider) audio.EngineProvider {
	if off(p, models.ModalityAudio) {
		return nil
	}
	d, _ := mr.GetDriver(p.Kind).(audio.EngineProvider)
	return d
}

// webSearcher is implemented by a driver whose API has built-in web search the harness can
// switch on for a request (Gemini's Grounding with Google Search).
type webSearcher interface{ WebSearch() bool }

// WebSearchOn reports whether built-in web search is switched on for the provider: its
// driver offers it AND the provider's web modality is enabled. Unlike the other modalities
// it is never on by default.
func (mr *ModelRouter) WebSearchOn(p *models.ModelProvider) bool {
	if w, ok := mr.GetDriver(p.Kind).(webSearcher); !ok || !w.WebSearch() {
		return false
	}
	on, set := p.ModalityEnabled(models.ModalityWeb)
	return set && on
}

func off(p *models.ModelProvider, modality string) bool {
	on, set := p.ModalityEnabled(modality)
	return set && !on
}

// Supports reports whether an agent running model on provider p can use the
// modality.
func (mr *ModelRouter) Supports(p *models.ModelProvider, model, modality string) bool {
	switch modality {
	case models.ModalityText:
		return true
	case models.ModalityAudio:
		return mr.SpeechFor(p) != nil
	case models.ModalityRealtime:
		return mr.RealtimeFor(p) != nil
	case models.ModalityWeb:
		return mr.WebSearchOn(p)
	}
	caps := mr.mediaCapsFor(p, model)
	switch modality {
	case models.ModalityImage:
		return caps.Vision
	case models.ModalityPDF:
		return !caps.PDFOff // native, or extracted text when the model has no PDF input
	case models.ModalityVideo:
		return caps.Video
	}
	return false
}

// Modalities lists what an agent running model on provider p can use.
func (mr *ModelRouter) Modalities(p *models.ModelProvider, model string) []string {
	var out []string
	for _, m := range models.AllModalities {
		if mr.Supports(p, model, m) {
			out = append(out, m)
		}
	}
	return out
}

// CheckModalities validates a provider's modalities config: its shape, and
// that audio and realtime are not configured on for a driver that cannot do
// them (turning one off is always fine).
func (mr *ModelRouter) CheckModalities(p *models.ModelProvider) error {
	if err := models.ValidateModalities(p.Config); err != nil {
		return err
	}
	driver := mr.GetDriver(p.Kind)
	if w, ok := driver.(webSearcher); (!ok || !w.WebSearch()) && p.ModalityConfigured(models.ModalityWeb) && !off(p, models.ModalityWeb) {
		return fmt.Errorf("config.modalities.web: provider kind %q has no built-in web search", p.Kind)
	}
	_, speech := driver.(audio.EngineProvider)
	_, live := driver.(realtime.Driver)
	for _, c := range []struct {
		modality string
		capable  bool
		what     string
	}{
		{models.ModalityAudio, speech, "speech API"},
		{models.ModalityRealtime, live, "realtime API"},
	} {
		if !c.capable && p.ModalityConfigured(c.modality) && !off(p, c.modality) {
			return fmt.Errorf("config.modalities.%s: provider kind %q has no %s", c.modality, p.Kind, c.what)
		}
	}
	return nil
}
