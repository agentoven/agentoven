package models

import (
	"fmt"
	"net/url"
	"strings"
)

// BotTier is how much of a machine a bot is given.
type BotTier string

const (
	// BotTierBrowser is a headless Chromium driven over the DevTools protocol (the default).
	BotTierBrowser BotTier = "browser"
	// BotTierDesktop is a full virtual X desktop driven by synthetic input.
	BotTierDesktop BotTier = "desktop"
)

// BotApprovalMode decides which of a bot's actions wait for a person.
type BotApprovalMode string

const (
	// BotApproveCommit asks a person before actions that are hard to undo or cross a trust
	// boundary: submitting forms, purchases, deletes, sign-in, credentials, files in or out, and
	// a first visit to a new site. This is the default.
	BotApproveCommit BotApprovalMode = "commit"
	// BotApproveAll asks before every action that changes anything (everything but observing).
	BotApproveAll BotApprovalMode = "all"
	// BotApproveOff never asks. Actions are still allowed, denied, journaled and audited by
	// policy; only the human step is removed. Use only for sandboxes holding nothing of value.
	BotApproveOff BotApprovalMode = "off"
)

// BotConfig is the part of an agent definition that applies only to a computer-use bot.
// The Pro control plane reads it when it spawns the sandbox and on every action the bot asks to
// take; the bot itself is told only what it needs.
type BotConfig struct {
	Tier BotTier `json:"tier,omitempty"`
	// StartURL is the first page the bot opens. Its host is always allowed.
	StartURL string `json:"start_url,omitempty"`
	// AllowedDomains is the egress allowlist: exact hosts ("example.com"), subdomain wildcards
	// ("*.example.com") or "*". Empty means only the host of StartURL.
	AllowedDomains []string `json:"allowed_domains,omitempty"`
	// BlockedDomains always wins over AllowedDomains.
	BlockedDomains []string `json:"blocked_domains,omitempty"`
	// Viewport is the screen size in pixels (width, height). Default 1280x800.
	ViewportWidth  int `json:"viewport_width,omitempty"`
	ViewportHeight int `json:"viewport_height,omitempty"`
	// MaxActions caps the actions in one run (default 200; the platform maximum is 5000).
	MaxActions int `json:"max_actions,omitempty"`
	// MaxRunSeconds caps a run's wall-clock time (default 1800).
	MaxRunSeconds int `json:"max_run_seconds,omitempty"`
	// Approval decides which actions wait for a person (default "commit").
	Approval BotApprovalMode `json:"approval,omitempty"`
	// ApprovalTimeoutSeconds is how long an approval waits before it expires and the action is
	// denied (default 900).
	ApprovalTimeoutSeconds int `json:"approval_timeout_seconds,omitempty"`
	// Credentials names the secret references the bot may type with type_secret. The bot never
	// sees the values; the control plane releases one only for a listed reference.
	Credentials []string `json:"credentials,omitempty"`
	// Image overrides the sandbox image. Empty uses the platform's ao-bot image.
	Image string `json:"image,omitempty"`
	// Sandbox overrides parts of the sandbox profile within what the platform allows.
	Memory string `json:"memory,omitempty"` // e.g. "2g"
	CPUs   string `json:"cpus,omitempty"`   // e.g. "1.5"
	// EvidenceRetentionDays is how long screenshots and snapshots are kept (default 30).
	EvidenceRetentionDays int `json:"evidence_retention_days,omitempty"`

	// The settings below give a bot a whole computer, not just a browser. Each is a request: the
	// kitchen's bot policy is the ceiling and the control plane applies the lower of the two.

	// Shell lets the bot run commands in its sandbox workspace. Nil means no shell.
	Shell *BotShell `json:"shell,omitempty"`
	// Files lets the bot read and write files in its workspace. Nil means no file access.
	Files *BotFiles `json:"files,omitempty"`
	// Persistence keeps the bot's workspace between runs: "none" (default), "workspace" (its files)
	// or "profile" (its files and its browser profile, so sign-ins survive). One snapshot per
	// agent, never shared with another agent, encrypted at rest by the control plane.
	Persistence BotPersistence `json:"persistence,omitempty"`
	// PersistenceMaxMB caps the snapshot (default 256, platform maximum 1024).
	PersistenceMaxMB int `json:"persistence_max_mb,omitempty"`
	// Takeover lets a person take control of a running sandbox through the control plane. It
	// defaults to allowed; "off" refuses it for this agent.
	Takeover BotTakeover `json:"takeover,omitempty"`
	// Tools, when set, narrows the kitchen tools (and skill tools) the bot is offered to these
	// names. Empty offers every tool the agent resolves.
	Tools []string `json:"tools,omitempty"`
}

// BotShell configures shell access.
type BotShell struct {
	Enabled bool `json:"enabled"`
	// Approval is "commit" (default: ask before commands that are hard to undo, or that leave the
	// workspace) or "each" (ask before every command).
	Approval string `json:"approval,omitempty"`
	// TimeoutSeconds bounds one command (default 120, maximum 900).
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
	// MaxOutputKB bounds the output kept from one command (default 64).
	MaxOutputKB int `json:"max_output_kb,omitempty"`
}

// BotFiles configures file access.
type BotFiles struct {
	Enabled bool `json:"enabled"`
	// MaxFileMB bounds one file read or written (default 8, maximum 64).
	MaxFileMB int `json:"max_file_mb,omitempty"`
}

// BotPersistence is how much of a bot's computer survives between runs.
type BotPersistence string

const (
	BotPersistNone      BotPersistence = "none"
	BotPersistWorkspace BotPersistence = "workspace"
	BotPersistProfile   BotPersistence = "profile"
)

// BotTakeover says whether a person may take control of this bot's sandbox.
type BotTakeover string

const (
	BotTakeoverAllowed BotTakeover = "allowed"
	BotTakeoverOff     BotTakeover = "off"
)

// Platform limits a BotConfig cannot exceed.
const (
	BotMaxActionsCeiling    = 5000
	BotMaxRunSecondsCeiling = 6 * 60 * 60
	BotPersistenceCeilingMB = 1024
)

// WithDefaults returns a copy with every unset field given its default.
func (c BotConfig) WithDefaults() BotConfig {
	if c.Tier == "" {
		c.Tier = BotTierBrowser
	}
	if c.Approval == "" {
		c.Approval = BotApproveCommit
	}
	if c.ViewportWidth <= 0 {
		c.ViewportWidth = 1280
	}
	if c.ViewportHeight <= 0 {
		c.ViewportHeight = 800
	}
	if c.MaxActions <= 0 {
		c.MaxActions = 200
	}
	if c.MaxRunSeconds <= 0 {
		c.MaxRunSeconds = 1800
	}
	if c.ApprovalTimeoutSeconds <= 0 {
		c.ApprovalTimeoutSeconds = 900
	}
	if c.EvidenceRetentionDays <= 0 {
		c.EvidenceRetentionDays = 30
	}
	if c.Persistence == "" {
		c.Persistence = BotPersistNone
	}
	if c.PersistenceMaxMB <= 0 {
		c.PersistenceMaxMB = 256
	}
	if c.Takeover == "" {
		c.Takeover = BotTakeoverAllowed
	}
	if c.Shell != nil {
		sh := *c.Shell
		if sh.Approval == "" {
			sh.Approval = "commit"
		}
		if sh.TimeoutSeconds <= 0 {
			sh.TimeoutSeconds = 120
		}
		if sh.MaxOutputKB <= 0 {
			sh.MaxOutputKB = 64
		}
		c.Shell = &sh
	}
	if c.Files != nil {
		f := *c.Files
		if f.MaxFileMB <= 0 {
			f.MaxFileMB = 8
		}
		c.Files = &f
	}
	return c
}

// Validate reports the first problem with the configuration.
func (c BotConfig) Validate() error {
	switch c.Tier {
	case "", BotTierBrowser, BotTierDesktop:
	default:
		return fmt.Errorf("bot.tier %q is not one of browser, desktop", c.Tier)
	}
	switch c.Approval {
	case "", BotApproveCommit, BotApproveAll, BotApproveOff:
	default:
		return fmt.Errorf("bot.approval %q is not one of commit, all, off", c.Approval)
	}
	if c.StartURL != "" {
		u, err := url.Parse(c.StartURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
			return fmt.Errorf("bot.start_url %q must be an http or https URL", c.StartURL)
		}
	}
	if c.MaxActions < 0 || c.MaxActions > BotMaxActionsCeiling {
		return fmt.Errorf("bot.max_actions must be between 0 and %d", BotMaxActionsCeiling)
	}
	if c.MaxRunSeconds < 0 || c.MaxRunSeconds > BotMaxRunSecondsCeiling {
		return fmt.Errorf("bot.max_run_seconds must be between 0 and %d", BotMaxRunSecondsCeiling)
	}
	if (c.ViewportWidth != 0 && (c.ViewportWidth < 320 || c.ViewportWidth > 3840)) ||
		(c.ViewportHeight != 0 && (c.ViewportHeight < 240 || c.ViewportHeight > 2160)) {
		return fmt.Errorf("bot viewport must be between 320x240 and 3840x2160")
	}
	switch c.Persistence {
	case "", BotPersistNone, BotPersistWorkspace, BotPersistProfile:
	default:
		return fmt.Errorf("bot.persistence %q is not one of none, workspace, profile", c.Persistence)
	}
	if c.PersistenceMaxMB < 0 || c.PersistenceMaxMB > BotPersistenceCeilingMB {
		return fmt.Errorf("bot.persistence_max_mb must be between 0 and %d", BotPersistenceCeilingMB)
	}
	if c.Persistence == BotPersistProfile && c.Tier == BotTierDesktop {
		return fmt.Errorf("bot.persistence \"profile\" is a browser-tier feature; use \"workspace\" on the desktop tier")
	}
	switch c.Takeover {
	case "", BotTakeoverAllowed, BotTakeoverOff:
	default:
		return fmt.Errorf("bot.takeover %q is not one of allowed, off", c.Takeover)
	}
	if sh := c.Shell; sh != nil {
		if sh.Approval != "" && sh.Approval != "commit" && sh.Approval != "each" {
			return fmt.Errorf("bot.shell.approval %q is not one of commit, each", sh.Approval)
		}
		if sh.TimeoutSeconds < 0 || sh.TimeoutSeconds > 900 || sh.MaxOutputKB < 0 || sh.MaxOutputKB > 1024 {
			return fmt.Errorf("bot.shell: timeout_seconds must be 0-900 and max_output_kb 0-1024")
		}
	}
	if f := c.Files; f != nil && (f.MaxFileMB < 0 || f.MaxFileMB > 64) {
		return fmt.Errorf("bot.files.max_file_mb must be between 0 and 64")
	}
	for _, t := range c.Tools {
		if strings.TrimSpace(t) == "" {
			return fmt.Errorf("bot.tools must not contain an empty name")
		}
	}
	for _, d := range append(append([]string{}, c.AllowedDomains...), c.BlockedDomains...) {
		if strings.TrimSpace(d) == "" || strings.ContainsAny(d, "/: ") {
			return fmt.Errorf("bot domain %q must be a bare host such as example.com or *.example.com", d)
		}
	}
	return nil
}

// SandboxProfile is how a bot's process is confined. The Pro bot manager decides it; the
// process executors (docker, k8s) apply it. A zero profile means "no extra confinement", which
// is what every non-bot agent gets.
type SandboxProfile struct {
	// Image is the sandbox image; empty keeps the executor's default.
	Image string `json:"image,omitempty"`
	// Confined turns on the settings below. Without it the rest is ignored.
	Confined       bool   `json:"confined,omitempty"`
	ReadOnlyRootFS bool   `json:"read_only_root_fs,omitempty"`
	User           string `json:"user,omitempty"`   // numeric uid:gid, e.g. "65532:65532"
	Memory         string `json:"memory,omitempty"` // "2g"
	CPUs           string `json:"cpus,omitempty"`   // "1.5"
	PidsLimit      int    `json:"pids_limit,omitempty"`
	ShmSize        string `json:"shm_size,omitempty"`   // Chromium needs more than the 64m default
	TmpfsSize      string `json:"tmpfs_size,omitempty"` // size of the writable scratch areas
	// RuntimeClass selects a stronger isolation runtime on Kubernetes (e.g. "gvisor", "kata").
	RuntimeClass string `json:"runtime_class,omitempty"`
	// ExtraHosts are host:ip entries so the sandbox can reach the control plane by name.
	ExtraHosts []string `json:"extra_hosts,omitempty"`
	// AllowEgressTo lists host:port destinations a Kubernetes NetworkPolicy leaves open (the
	// control plane's gate and egress proxy). Everything else is closed.
	AllowEgressTo []string `json:"allow_egress_to,omitempty"`
	// NetworkLockdown asks the docker executor's caller to confine the started container's network
	// to AllowEgressTo, through contracts.ContainerConfiner. Without it a container's egress is only
	// as confined as the Docker network it runs on, and the proxy is advisory.
	NetworkLockdown bool `json:"network_lockdown,omitempty"`
}
