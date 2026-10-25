package models

// SubagentSpec describes a subagent a running agent asks for on the spot, as opposed to
// delegating to an agent that is registered (agentoven_delegate). The model fills it in as the
// arguments of the `task` tool; nothing in it is trusted. In particular it can only narrow what
// its parent has (a subset of the parent's tools and skills, a share of the parent's budget),
// never widen it.
type SubagentSpec struct {
	// Name labels the subagent in traces and audit events.
	Name string `json:"name,omitempty"`
	// Description says what the subagent is for. It is shown to a person approving the spawn.
	Description string `json:"description,omitempty"`
	// SystemPrompt is the subagent's instructions. It does not see the parent's conversation.
	SystemPrompt string `json:"system_prompt,omitempty"`
	// Model overrides the parent's model. It must be one the kitchen's routing can reach.
	Model string `json:"model,omitempty"`
	// Tools names which of the parent's tools the subagent may use. Empty means none; "*"
	// means all of them.
	Tools []string `json:"tools,omitempty"`
	// Skills names which of the parent's skills the subagent may use.
	Skills []string `json:"skills,omitempty"`
	// Budget asks for a cap smaller than the share the parent would grant.
	Budget *RunBudget `json:"budget,omitempty"`
	// Input is the task: the subagent's one user message.
	Input string `json:"input"`
}

// SubagentResult is what a subagent hands back to its parent: only its final answer, not the
// conversation that produced it.
type SubagentResult struct {
	Name    string     `json:"name"`
	TraceID string     `json:"trace_id,omitempty"`
	Output  string     `json:"output,omitempty"`
	IsError bool       `json:"is_error,omitempty"`
	Error   string     `json:"error,omitempty"`
	Usage   TokenUsage `json:"usage"`
	Turns   int        `json:"turns,omitempty"`
}

// SubagentParent identifies the run that is spawning.
type SubagentParent struct {
	Agent   *Agent
	TraceID string
}
