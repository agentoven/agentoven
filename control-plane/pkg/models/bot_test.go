package models

import "testing"

func TestBotConfigDefaults(t *testing.T) {
	c := BotConfig{}.WithDefaults()
	if c.Tier != BotTierBrowser || c.Approval != BotApproveCommit || c.ViewportWidth != 1280 || c.ViewportHeight != 800 ||
		c.MaxActions != 200 || c.MaxRunSeconds != 1800 || c.ApprovalTimeoutSeconds != 900 || c.EvidenceRetentionDays != 30 {
		t.Fatalf("unexpected defaults: %+v", c)
	}
	// Explicit values survive.
	c = BotConfig{MaxActions: 7, Approval: BotApproveAll}.WithDefaults()
	if c.MaxActions != 7 || c.Approval != BotApproveAll {
		t.Fatalf("explicit values were overwritten: %+v", c)
	}
}

func TestBotConfigValidate(t *testing.T) {
	bad := []BotConfig{
		{Tier: "vm"},
		{Approval: "sometimes"},
		{StartURL: "ftp://x.example"},
		{StartURL: "not a url"},
		{MaxActions: BotMaxActionsCeiling + 1},
		{MaxRunSeconds: BotMaxRunSecondsCeiling + 1},
		{ViewportWidth: 100, ViewportHeight: 800},
		{AllowedDomains: []string{"https://x.example"}},
		{BlockedDomains: []string{"x.example/path"}},
		{AllowedDomains: []string{" "}},
	}
	for i, c := range bad {
		if c.Validate() == nil {
			t.Errorf("case %d (%+v) should be invalid", i, c)
		}
	}
	good := BotConfig{Tier: BotTierDesktop, StartURL: "https://shop.example/", AllowedDomains: []string{"shop.example", "*.cdn.example"}, MaxActions: 50}
	if err := good.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

func TestAgenticBotIsFrameworkNative(t *testing.T) {
	a := &Agent{Runtime: RuntimeAgenticBot}
	if !a.IsAgenticBot() || !a.IsFrameworkNative() {
		t.Fatal("a bot is hosted in its own process and so framework-native")
	}
	if (&Agent{Runtime: RuntimeLangGraph}).IsAgenticBot() {
		t.Fatal("only the agenticbot runtime is a bot")
	}
}

func TestBotConfigBreadthSettings(t *testing.T) {
	c := BotConfig{Shell: &BotShell{Enabled: true}, Files: &BotFiles{Enabled: true}}.WithDefaults()
	if c.Persistence != BotPersistNone || c.PersistenceMaxMB != 256 || c.Takeover != BotTakeoverAllowed ||
		c.Shell.Approval != "commit" || c.Shell.TimeoutSeconds != 120 || c.Shell.MaxOutputKB != 64 || c.Files.MaxFileMB != 8 {
		t.Fatalf("defaults: %+v shell=%+v files=%+v", c, c.Shell, c.Files)
	}
	// WithDefaults must not mutate the caller's nested config.
	in := &BotShell{Enabled: true}
	_ = BotConfig{Shell: in}.WithDefaults()
	if in.TimeoutSeconds != 0 {
		t.Fatal("WithDefaults mutated the original shell config")
	}
	bad := []BotConfig{
		{Persistence: "forever"},
		{PersistenceMaxMB: BotPersistenceCeilingMB + 1},
		{Tier: BotTierDesktop, Persistence: BotPersistProfile},
		{Takeover: "sometimes"},
		{Shell: &BotShell{Enabled: true, Approval: "never"}},
		{Shell: &BotShell{Enabled: true, TimeoutSeconds: 901}},
		{Files: &BotFiles{Enabled: true, MaxFileMB: 65}},
		{Tools: []string{"ok", " "}},
	}
	for i, b := range bad {
		if b.Validate() == nil {
			t.Errorf("case %d should be invalid: %+v", i, b)
		}
	}
	good := BotConfig{Persistence: BotPersistProfile, Shell: &BotShell{Enabled: true, Approval: "each", TimeoutSeconds: 60}, Files: &BotFiles{Enabled: true}, Tools: []string{"crm_find"}}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
}
