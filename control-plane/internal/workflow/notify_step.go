package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/agentoven/agentoven/control-plane/internal/notify"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

var placeholder = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_.\-]+)\s*\}\}`)

// renderTemplate fills {{name}} and {{path.to.value}} in text from env. A name that has no value
// becomes empty text rather than staying in the message as braces.
func renderTemplate(text string, env map[string]interface{}) string {
	return placeholder.ReplaceAllStringFunc(text, func(m string) string {
		path := placeholder.FindStringSubmatch(m)[1]
		v, ok := resolveJSONPath(env, path)
		if !ok || v == nil {
			return ""
		}
		switch t := v.(type) {
		case string:
			return t
		case float64, int, int64, bool:
			return fmt.Sprint(t)
		default:
			b, err := json.Marshal(t)
			if err != nil {
				return ""
			}
			return string(b)
		}
	})
}

// notifyTargets are the channels (settings.channels) and tools (notify_tools) a notify step sends to.
func notifyTargets(step *models.Step) []string {
	var channels []string
	if step.Config != nil {
		switch v := step.Config["channels"].(type) {
		case []interface{}:
			for _, c := range v {
				if s, ok := c.(string); ok && strings.TrimSpace(s) != "" {
					channels = append(channels, strings.TrimSpace(s))
				}
			}
		case []string:
			channels = append(channels, v...)
		}
	}
	return channels
}

// ValidateNotifyStep says what is wrong with a notify step's settings, so a recipe can be refused when it
// is saved: it needs a message, and somewhere to send it.
func ValidateNotifyStep(step *models.Step) error {
	msg, _ := step.Config["message"].(string)
	if strings.TrimSpace(msg) == "" {
		return fmt.Errorf("notify step '%s' needs a message", step.Name)
	}
	if len(notifyTargets(step)) == 0 && len(step.NotifyTools) == 0 {
		return fmt.Errorf("notify step '%s' needs a channel or a tool to send to", step.Name)
	}
	if of, _ := step.Config["on_failure"].(string); of != "" && of != "stop" && of != "carry_on" {
		return fmt.Errorf("notify step '%s': on_failure must be stop or carry_on", step.Name)
	}
	return nil
}

// executeNotify pushes a message to the channels and tools the step names, then lets the run carry on.
// It is how a recipe tells someone something at a chosen point: that a person is needed before an
// approval, that a stage is done, that something went the unusual way.
//
// The message and subject may use {{run_id}}, {{recipe}}, {{step}}, {{text}} (what the steps before
// said), {{input.field}} and {{step-name.field}}. The step fails if any target could not be reached,
// unless its on_failure is carry_on. A WebSocket channel nobody is listening to is not a failure: the
// result says how many connections it reached.
func (e *Engine) executeNotify(ctx context.Context, run *models.RecipeRun, step *models.Step, result *models.StepResult, completed map[string]*models.StepResult, mu *sync.Mutex) error {
	if err := ValidateNotifyStep(step); err != nil {
		return err
	}
	if e.notifier == nil {
		return fmt.Errorf("notify step '%s': notifications are not available", step.Name)
	}

	recipe := run.RecipeID
	if v, ok := e.runRecipeNames.Load(run.ID); ok {
		recipe, _ = v.(string)
	}

	env := map[string]interface{}{"run_id": run.ID, "recipe": recipe, "step": step.Name}
	if run.Input != nil {
		env["input"] = run.Input
	}
	var texts []string
	mu.Lock()
	for _, dep := range step.DependsOn {
		if sr, ok := completed[dep]; ok && sr.Output != nil {
			env[dep] = sr.Output
			for k, v := range sr.Output {
				if _, taken := env[k]; !taken {
					env[k] = v
				}
			}
			if t := extractTextFromA2AOutput(sr.Output); t != "" {
				texts = append(texts, t)
			}
		}
	}
	mu.Unlock()
	if _, has := env["text"]; !has {
		env["text"] = strings.Join(texts, "\n")
	}

	message := renderTemplate(fmt.Sprint(step.Config["message"]), env)
	payload := map[string]interface{}{"message": message}
	if subject, _ := step.Config["subject"].(string); strings.TrimSpace(subject) != "" {
		payload["subject"] = renderTemplate(subject, env)
	}
	if extra, ok := step.Config["payload"].(map[string]interface{}); ok {
		for k, v := range extra {
			if _, reserved := payload[k]; !reserved {
				if s, isText := v.(string); isText {
					v = renderTemplate(s, env)
				}
				payload[k] = v
			}
		}
	}

	event := notify.NewEvent(notify.EventNotification, run.Kitchen, run.ID, recipe, step.Name, payload)
	results := e.notifier.SendTo(ctx, run.Kitchen, notifyTargets(step), step.NotifyTools, event)
	result.NotifyResults = results

	sent, failed := 0, 0
	var reasons []string
	for _, r := range results {
		if r.Success {
			sent++
		} else {
			failed++
			reasons = append(reasons, fmt.Sprintf("%s: %s", r.Tool, r.Error))
		}
	}
	result.Output = map[string]interface{}{"message": message, "sent": sent, "failed": failed}
	if failed > 0 {
		if of, _ := step.Config["on_failure"].(string); of != "carry_on" {
			return fmt.Errorf("notify step '%s' could not reach %d of %d targets: %s", step.Name, failed, len(results), strings.Join(reasons, "; "))
		}
	}
	return nil
}
