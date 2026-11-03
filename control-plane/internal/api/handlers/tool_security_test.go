package handlers_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/agentoven/agentoven/control-plane/pkg/netguard"
)

func TestAToolRegisteredSwitchedOffStaysOffAndAPartialUpdateDoesNotSwitchItOff(t *testing.T) {
	h, s := newTestHandlers(t)
	ctx := context.Background()
	reg := func(body string) int {
		return callAgentAPI(h.RegisterMCPTool, http.MethodPost, "/api/v1/tools", nil, body).Code
	}
	if c := reg(`{"name":"off","endpoint":"https://tools.example.test/mcp","enabled":false}`); c != http.StatusCreated {
		t.Fatalf("register off: %d", c)
	}
	if c := reg(`{"name":"on","endpoint":"https://tools.example.test/mcp"}`); c != http.StatusCreated {
		t.Fatalf("register on: %d", c)
	}
	if tool, _ := s.GetTool(ctx, "default", "off"); tool == nil || tool.Enabled {
		t.Errorf("a tool registered with enabled=false must be off: %+v", tool)
	}
	if tool, _ := s.GetTool(ctx, "default", "on"); tool == nil || !tool.Enabled {
		t.Errorf("a tool registered without the field is on: %+v", tool)
	}
	// A PUT that does not mention enabled leaves it alone (it used to switch the tool off).
	callAgentAPI(h.UpdateMCPTool, http.MethodPut, "/api/v1/tools/on", map[string]string{"toolName": "on"}, `{"description":"changed"}`)
	if tool, _ := s.GetTool(ctx, "default", "on"); !tool.Enabled || tool.Description != "changed" {
		t.Errorf("a partial update must not change enabled: %+v", tool)
	}
	callAgentAPI(h.UpdateMCPTool, http.MethodPut, "/api/v1/tools/on", map[string]string{"toolName": "on"}, `{"enabled":false}`)
	if tool, _ := s.GetTool(ctx, "default", "on"); tool.Enabled {
		t.Error("an explicit enabled=false must switch the tool off")
	}
}

func TestWritingBackTheMaskedAuthConfigKeepsTheStoredSecret(t *testing.T) {
	h, s := newTestHandlers(t) // no vault: the literal stays in the row, as in the community edition
	ctx := context.Background()
	callAgentAPI(h.RegisterMCPTool, http.MethodPost, "/api/v1/tools", nil,
		`{"name":"keyed","endpoint":"https://tools.example.test/mcp","auth_config":{"type":"bearer","token":"SECRET-AUTH"}}`)
	got := callAgentAPI(h.GetMCPTool, http.MethodGet, "/api/v1/tools/keyed", map[string]string{"toolName": "keyed"}, "")
	if strings.Contains(got.Body.String(), "SECRET-AUTH") || !strings.Contains(got.Body.String(), `"token":"****"`) {
		t.Fatalf("GET must show the mask: %s", got.Body)
	}
	// A client that reads, edits and writes the tool back sends the mask.
	callAgentAPI(h.UpdateMCPTool, http.MethodPut, "/api/v1/tools/keyed", map[string]string{"toolName": "keyed"},
		`{"description":"edited","auth_config":{"type":"bearer","token":"****"}}`)
	tool, _ := s.GetTool(ctx, "default", "keyed")
	if tok, _ := tool.AuthConfig["token"].(string); tok != "SECRET-AUTH" {
		t.Errorf("the mask replaced the secret: %v", tool.AuthConfig)
	}
	// A real new value replaces it, and a mask with nothing stored is dropped, never stored.
	callAgentAPI(h.UpdateMCPTool, http.MethodPut, "/api/v1/tools/keyed", map[string]string{"toolName": "keyed"},
		`{"auth_config":{"type":"bearer","token":"ROTATED-1234"}}`)
	if tool, _ = s.GetTool(ctx, "default", "keyed"); tool.AuthConfig["token"] != "ROTATED-1234" {
		t.Errorf("a new secret must be stored: %v", tool.AuthConfig)
	}
	callAgentAPI(h.RegisterMCPTool, http.MethodPost, "/api/v1/tools", nil,
		`{"name":"fresh","endpoint":"https://tools.example.test/mcp","auth_config":{"type":"bearer","token":"****"}}`)
	if tool, _ = s.GetTool(ctx, "default", "fresh"); tool.AuthConfig["token"] != nil {
		t.Errorf("a mask is never a value: %v", tool.AuthConfig)
	}
}

func TestAToolsSecretIsSealedInTheVaultAndTheRowHoldsAReference(t *testing.T) {
	h, _, _ := vaultHandlers(t)
	st := h.Store
	ctx := context.Background()
	w := callAgentAPI(h.RegisterMCPTool, http.MethodPost, "/api/v1/tools", nil,
		`{"name":"sealed","endpoint":"https://tools.example.test/mcp","auth_config":{"type":"api-key","header":"X-Api-Key","key":"LIVE-KEY-9999"}}`)
	if w.Code != http.StatusCreated || strings.Contains(w.Body.String(), "LIVE-KEY") {
		t.Fatalf("register: %d %s", w.Code, w.Body)
	}
	tool, _ := st.GetTool(ctx, "default", "sealed")
	if _, literal := tool.AuthConfig["key"]; literal || tool.AuthConfig["credential_ref"] != "tool-sealed" || tool.AuthConfig["header"] != "X-Api-Key" || tool.AuthConfig["type"] != "api-key" {
		t.Fatalf("the row must hold a reference, not the key: %v", tool.AuthConfig)
	}
	if c, err := st.GetKitchenCredential(ctx, "default", "tool-sealed"); err != nil || c.Value != "LIVE-KEY-9999" {
		t.Fatalf("the credential holds the key: %v %v", c, err)
	}
	// Writing the reference back changes nothing; a new literal rotates the credential in place.
	callAgentAPI(h.UpdateMCPTool, http.MethodPut, "/api/v1/tools/sealed", map[string]string{"toolName": "sealed"},
		`{"auth_config":{"type":"api-key","header":"X-Api-Key","credential_ref":"tool-sealed"}}`)
	if c, _ := st.GetKitchenCredential(ctx, "default", "tool-sealed"); c.Value != "LIVE-KEY-9999" {
		t.Errorf("a reference written back must not change the secret: %q", c.Value)
	}
	callAgentAPI(h.UpdateMCPTool, http.MethodPut, "/api/v1/tools/sealed", map[string]string{"toolName": "sealed"},
		`{"auth_config":{"type":"api-key","header":"X-Api-Key","key":"ROTATED-8888"}}`)
	if c, _ := st.GetKitchenCredential(ctx, "default", "tool-sealed"); c.Value != "ROTATED-8888" {
		t.Errorf("a new literal rotates the credential: %q", c.Value)
	}
	// A failed create does not leave the credential behind, and deleting the tool removes it.
	callAgentAPI(h.RegisterMCPTool, http.MethodPost, "/api/v1/tools", nil,
		`{"name":"sealed","endpoint":"https://tools.example.test/mcp","auth_config":{"type":"bearer","token":"OTHER-TOKEN"}}`)
	callAgentAPI(h.DeleteMCPTool, http.MethodDelete, "/api/v1/tools/sealed", map[string]string{"toolName": "sealed"}, "")
	if _, err := st.GetKitchenCredential(ctx, "default", "tool-sealed"); err == nil {
		t.Error("deleting the tool must delete its credential")
	}
}

func TestAToolCannotBePointedAtTheControlPlanesOwnNetwork(t *testing.T) {
	t.Setenv(netguard.EnvAllow, "") // the package's TestMain allows loopback for the other tests
	h, s := newTestHandlers(t)
	ctx := context.Background()
	for _, ep := range []string{"http://169.254.169.254/latest/meta-data/", "http://127.0.0.1:8081/api/v1/agents", "http://10.0.0.5/mcp", "http://[::1]/", "file:///etc/passwd", "http://user:pw@tools.example.test/"} {
		w := callAgentAPI(h.RegisterMCPTool, http.MethodPost, "/api/v1/tools", nil, `{"name":"ssrf","endpoint":"`+ep+`"}`)
		if w.Code != http.StatusBadRequest {
			t.Errorf("register %s: %d, want 400", ep, w.Code)
		}
	}
	if tool, _ := s.GetTool(ctx, "default", "ssrf"); tool != nil {
		t.Error("a refused tool must not be stored")
	}
	if c := callAgentAPI(h.RegisterMCPTool, http.MethodPost, "/api/v1/tools", nil, `{"name":"ok","endpoint":"https://93.184.216.34/mcp"}`).Code; c != http.StatusCreated {
		t.Fatalf("a public endpoint: %d", c)
	}
	// An update to a bad endpoint, and a bulk with one bad endpoint (nothing at all is created).
	if c := callAgentAPI(h.UpdateMCPTool, http.MethodPut, "/api/v1/tools/ok", map[string]string{"toolName": "ok"}, `{"endpoint":"http://169.254.169.254/"}`).Code; c != http.StatusBadRequest {
		t.Errorf("update to metadata: %d", c)
	}
	if tool, _ := s.GetTool(ctx, "default", "ok"); tool.Endpoint != "https://93.184.216.34/mcp" {
		t.Errorf("the endpoint must be unchanged: %s", tool.Endpoint)
	}
	bulk := `{"tools":[{"name":"b1","endpoint":"https://93.184.216.34/a"},{"name":"b2","endpoint":"http://169.254.169.254/"}]}`
	if c := callAgentAPI(h.BulkRegisterMCPTools, http.MethodPost, "/api/v1/tools/bulk", nil, bulk).Code; c != http.StatusBadRequest {
		t.Errorf("bulk with a bad endpoint: %d", c)
	}
	if tool, _ := s.GetTool(ctx, "default", "b1"); tool != nil {
		t.Error("a bulk with a bad member must create nothing")
	}
	// The operator's allowlist lets an internal server through.
	t.Setenv(netguard.EnvAllow, "10.0.0.5")
	if c := callAgentAPI(h.RegisterMCPTool, http.MethodPost, "/api/v1/tools", nil, `{"name":"internal","endpoint":"http://10.0.0.5/mcp"}`).Code; c != http.StatusCreated {
		t.Errorf("an allowlisted host: %d", c)
	}
	// Bulk honours enabled too.
	if c := callAgentAPI(h.BulkRegisterMCPTools, http.MethodPost, "/api/v1/tools/bulk", nil, `{"tools":[{"name":"bo","endpoint":"https://93.184.216.34/a","enabled":false}]}`).Code; c != http.StatusCreated {
		t.Fatalf("bulk: %d", c)
	}
	if tool, _ := s.GetTool(ctx, "default", "bo"); tool == nil || tool.Enabled {
		t.Errorf("bulk enabled=false: %+v", tool)
	}
}

func TestToolsRegisteredBeforeTheVaultAreSealedByTheStartupPass(t *testing.T) {
	h, _, _ := vaultHandlers(t)
	st := h.Store
	ctx := context.Background()
	if ks, _ := st.ListKitchens(ctx); len(ks) == 0 {
		_ = st.CreateKitchen(ctx, &models.Kitchen{ID: "default", Name: "default"})
	}
	// A tool that predates sealing: the literal sits in its row.
	if err := st.CreateTool(ctx, &models.MCPTool{ID: "t1", Name: "old", Kitchen: "default", Endpoint: "https://tools.example.test/mcp", Transport: "http", Enabled: true,
		AuthConfig: map[string]interface{}{"type": "bearer", "token": "OLD-SECRET-1"}}); err != nil {
		t.Fatal(err)
	}
	rep, err := h.SealAllTools(ctx, true)
	if err != nil || len(rep.Moved) != 1 || rep.Moved[0] != "default/old" {
		t.Fatalf("dry run: %+v %v", rep, err)
	}
	if tool, _ := st.GetTool(ctx, "default", "old"); tool.AuthConfig["token"] != "OLD-SECRET-1" {
		t.Fatal("a dry run changes nothing")
	}
	rep, err = h.SealAllTools(ctx, false)
	if err != nil || len(rep.Moved) != 1 || len(rep.Failed) != 0 {
		t.Fatalf("seal: %+v %v", rep, err)
	}
	tool, _ := st.GetTool(ctx, "default", "old")
	if _, literal := tool.AuthConfig["token"]; literal || tool.AuthConfig["credential_ref"] != "tool-old" {
		t.Errorf("the row must hold a reference: %v", tool.AuthConfig)
	}
	if c, err := st.GetKitchenCredential(ctx, "default", "tool-old"); err != nil || c.Value != "OLD-SECRET-1" {
		t.Errorf("the credential holds the secret: %v %v", c, err)
	}
	if rep, _ = h.SealAllTools(ctx, false); len(rep.Moved) != 0 {
		t.Errorf("a second pass moves nothing: %+v", rep)
	}
	// Without a vault the pass says so instead of pretending.
	plain, _ := newTestHandlers(t)
	if _, err := plain.SealAllTools(ctx, false); err == nil {
		t.Error("no vault: want an error")
	}
}
