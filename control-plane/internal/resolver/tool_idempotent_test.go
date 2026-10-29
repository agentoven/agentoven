package resolver

import (
	"context"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// A tool is safe to repeat only if its MCP annotations say it is both read-only and idempotent. The resolver
// never set the flag before, so the executor's crash recovery and a replay's live mode could never tell.
func TestAToolIsResolvedAsSafeToRepeatOnlyWhenItSaysSo(t *testing.T) {
	s := newTestStore(t)
	r := NewResolver(s)
	ctx := context.Background()
	for name, schema := range map[string]map[string]interface{}{
		"reads":     {"annotations": map[string]interface{}{"readOnlyHint": true, "idempotentHint": true}},
		"read-only": {"annotations": map[string]interface{}{"readOnlyHint": true}},
		"idem-only": {"annotations": map[string]interface{}{"idempotentHint": true}},
		"writes":    {"annotations": map[string]interface{}{"readOnlyHint": false, "idempotentHint": true}},
		"silent":    {"type": "object"},
		"no-schema": nil,
	} {
		if err := s.CreateTool(ctx, &models.MCPTool{Name: name, Kitchen: "default", Endpoint: "http://x", Transport: "http", Enabled: true, Capabilities: []string{"tool"}, Schema: schema}); err != nil {
			t.Fatal(err)
		}
		got, err := r.resolveTool(ctx, "default", models.Ingredient{Name: name, Kind: models.IngredientTool})
		if err != nil {
			t.Fatal(err)
		}
		if want := name == "reads"; got.Idempotent != want {
			t.Errorf("%s: Idempotent = %v, want %v", name, got.Idempotent, want)
		}
	}
}
