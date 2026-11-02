package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// Dynamic subagents. The community edition lets an agent delegate to a registered agent
// (agentoven_delegate). An installed contracts.SubagentRuntime adds `task` and `tasks`, with
// which the model describes a subagent on the spot. The runtime owns policy; everything that
// makes a spawned subagent safe stays here, so it applies to any runtime:
//
//   - its spend is charged to the parent's budget as it happens, out of a share reserved for it
//     (so a fan-out cannot oversubscribe the parent);
//   - it can only narrow what the parent has: a subset of its tools and skills, never a tool the
//     parent has to ask a person to approve;
//   - it runs under the parent's guardrails, with its own journal naming the parent;
//   - it cannot spawn or delegate in turn, so the tree is one level deep;
//   - it does not see the parent's conversation, and the parent sees only its final answer.

const (
	subagentToolName  = "task"
	subagentsToolName = "tasks"
	// maxSpawnBatch bounds one `tasks` call whatever the runtime allows.
	maxSpawnBatch = 10
)

type (
	subagentCtxKey   struct{}
	parentScopeKey   struct{}
	subagentSequence struct{}
)

type parentScope struct {
	agent    *models.Agent
	resolved *models.ResolvedIngredients
	traceID  string
}

func withParentScope(ctx context.Context, s *parentScope) context.Context {
	return context.WithValue(ctx, parentScopeKey{}, s)
}

func parentScopeFrom(ctx context.Context) *parentScope {
	s, _ := ctx.Value(parentScopeKey{}).(*parentScope)
	return s
}

func isSubagent(ctx context.Context) bool {
	v, _ := ctx.Value(subagentCtxKey{}).(bool)
	return v
}

var subagentSeq atomic.Int64

// SetSubagentRuntime installs the runtime that offers agents the task and tasks tools. Pass
// nil to remove it.
func (e *Executor) SetSubagentRuntime(rt contracts.SubagentRuntime) {
	e.subagents = rt
}

// subagentsOffered reports whether an agent running under ctx is given the subagent tools.
func (e *Executor) subagentsOffered(ctx context.Context, agent *models.Agent) bool {
	return e.subagents != nil && !isSubagent(ctx) && e.subagents.Enabled(agent)
}

func specProperties() map[string]interface{} {
	str := func(desc string) map[string]interface{} {
		return map[string]interface{}{"type": "string", "description": desc}
	}
	strs := func(desc string) map[string]interface{} {
		return map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": desc}
	}
	return map[string]interface{}{
		"name":          str("A short label for this subagent."),
		"description":   str("What this subagent is for, in a sentence."),
		"system_prompt": str("The subagent's instructions. It cannot see this conversation, so include everything it needs to know."),
		"input":         str("The task for the subagent. Be specific about what you want back."),
		"model":         str("Optional: a model for the subagent. Defaults to yours."),
		"tools":         strs("Which of your tools the subagent may use, by name. Empty means none; [\"*\"] means all of yours."),
		"skills":        strs("Which of your skills the subagent may use, by name."),
		"budget": map[string]interface{}{
			"type": "object", "description": "Optional smaller limits for this subagent (it also cannot exceed its share of your budget).",
			"properties": map[string]interface{}{
				"max_tokens":         map[string]interface{}{"type": "integer"},
				"max_cost_usd":       map[string]interface{}{"type": "number"},
				"max_turns":          map[string]interface{}{"type": "integer"},
				"max_active_seconds": map[string]interface{}{"type": "integer"},
			},
		},
	}
}

func subagentToolDefs() []models.ToolDefinition {
	return []models.ToolDefinition{
		{Type: "function", Function: models.ToolFunction{
			Name: subagentToolName,
			Description: "Spawn a subagent to do one self-contained task and return only its final answer. Use it to keep your own context small " +
				"(research, searching, summarizing a large input) or to do independent work in parallel by calling it several times in one turn. " +
				"The subagent starts with a clean context, so give it everything it needs.",
			Parameters: map[string]interface{}{"type": "object", "properties": specProperties(), "required": []interface{}{"input"}},
		}},
		{Type: "function", Function: models.ToolFunction{
			Name:        subagentsToolName,
			Description: fmt.Sprintf("Spawn several subagents at once (at most %d). They share what is left of your budget equally and run in parallel. Returns each answer in order.", maxSpawnBatch),
			Parameters: map[string]interface{}{"type": "object", "properties": map[string]interface{}{
				"tasks": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "object", "properties": specProperties(), "required": []interface{}{"input"}}},
			}, "required": []interface{}{"tasks"}},
		}},
	}
}

// stripDelegation removes the tools a subagent must not have.
func stripDelegation(defs []models.ToolDefinition) []models.ToolDefinition {
	out := defs[:0:0]
	for _, d := range defs {
		switch d.Function.Name {
		case "agentoven_delegate", subagentToolName, subagentsToolName:
			continue
		}
		out = append(out, d)
	}
	return out
}

// executeSubagentTool runs one call of task or tasks.
func (e *Executor) executeSubagentTool(ctx context.Context, agent *models.Agent, tc ToolCall) ToolResult {
	fail := func(msg string) ToolResult {
		return ToolResult{ToolCallID: tc.ID, Name: tc.Name, IsError: true, Content: "Error: " + msg}
	}
	scope := parentScopeFrom(ctx)
	switch {
	case e.subagents == nil:
		return fail("subagents are not available on this server")
	case isSubagent(ctx):
		return fail("a subagent cannot spawn subagents")
	case scope == nil:
		return fail("subagents can only be spawned from inside a running agent")
	case !e.subagents.Enabled(agent):
		return fail("subagents are not enabled for this agent")
	}
	parent := models.SubagentParent{Agent: scope.agent, TraceID: scope.traceID}
	host := &subagentHost{e: e, scope: scope}

	if tc.Name == subagentToolName {
		var spec models.SubagentSpec
		if err := decodeArgs(tc.Arguments, &spec); err != nil {
			return fail("invalid arguments: " + err.Error())
		}
		res, err := e.subagents.Spawn(ctx, parent, spec, host)
		if err != nil {
			return ToolResult{ToolCallID: tc.ID, Name: tc.Name, IsError: true, Content: "Error: " + err.Error(), ChildUsage: res.Usage}
		}
		return resultOf(tc, res)
	}

	var batch struct {
		Tasks []models.SubagentSpec `json:"tasks"`
	}
	if err := decodeArgs(tc.Arguments, &batch); err != nil {
		return fail("invalid arguments: " + err.Error())
	}
	if len(batch.Tasks) == 0 {
		return fail("tasks must list at least one subagent")
	}
	if len(batch.Tasks) > maxSpawnBatch {
		return fail(fmt.Sprintf("at most %d subagents per call, got %d", maxSpawnBatch, len(batch.Tasks)))
	}
	results, err := e.subagents.SpawnMany(ctx, parent, batch.Tasks, host)
	var usage models.TokenUsage
	for _, r := range results {
		usage = addUsage(usage, r.Usage)
	}
	if err != nil {
		return ToolResult{ToolCallID: tc.ID, Name: tc.Name, IsError: true, Content: "Error: " + err.Error(), ChildUsage: usage}
	}
	type row struct {
		Name   string `json:"name"`
		Output string `json:"output,omitempty"`
		Error  string `json:"error,omitempty"`
	}
	rows := make([]row, len(results))
	failed := 0
	for i, r := range results {
		rows[i] = row{Name: r.Name, Output: r.Output, Error: r.Error}
		if r.IsError {
			failed++
		}
	}
	raw, _ := json.Marshal(rows)
	return ToolResult{ToolCallID: tc.ID, Name: tc.Name, Content: string(raw), IsError: failed == len(results), ChildUsage: usage}
}

func resultOf(tc ToolCall, r models.SubagentResult) ToolResult {
	if r.IsError {
		msg := r.Error
		if r.Output != "" {
			msg += "\nPartial output: " + r.Output
		}
		return ToolResult{ToolCallID: tc.ID, Name: tc.Name, IsError: true, Content: "Error: " + msg, ChildUsage: r.Usage}
	}
	return ToolResult{ToolCallID: tc.ID, Name: tc.Name, Content: r.Output, ChildUsage: r.Usage}
}

func decodeArgs(args map[string]interface{}, into interface{}) error {
	raw, err := json.Marshal(args)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, into)
}

// subagentHost is the executor's side of contracts.SubagentHost.
type subagentHost struct {
	e     *Executor
	scope *parentScope
}

func (h *subagentHost) Run(ctx context.Context, spec models.SubagentSpec) (models.SubagentResult, error) {
	if isSubagent(ctx) {
		return models.SubagentResult{}, fmt.Errorf("a subagent cannot spawn subagents")
	}
	child, resolved, err := buildSubagent(h.e.withPolicyGated(ctx, h.scope.agent, h.scope.resolved), h.scope.resolved, spec)
	if err != nil {
		return models.SubagentResult{Name: spec.Name}, err
	}
	var g *grant
	if parent := runBudgetFrom(ctx); parent != nil {
		if g, err = parent.grantFor(models.RunBudget{}); err != nil {
			return models.SubagentResult{Name: child.Name}, err
		}
		defer parent.release(g)
	}
	return h.e.runSubagent(ctx, child, resolved, spec, g), nil
}

func (h *subagentHost) RunMany(ctx context.Context, specs []models.SubagentSpec) []models.SubagentResult {
	results := make([]models.SubagentResult, len(specs))
	if isSubagent(ctx) {
		for i := range results {
			results[i] = models.SubagentResult{Name: specs[i].Name, IsError: true, Error: "a subagent cannot spawn subagents"}
		}
		return results
	}
	children := make([]*models.Agent, len(specs))
	resolved := make([]*models.ResolvedIngredients, len(specs))
	for i, spec := range specs {
		c, r, err := buildSubagent(h.e.withPolicyGated(ctx, h.scope.agent, h.scope.resolved), h.scope.resolved, spec)
		if err != nil {
			results[i] = models.SubagentResult{Name: spec.Name, IsError: true, Error: err.Error()}
			continue
		}
		children[i], resolved[i] = c, r
	}

	// Every subagent's share is reserved before any starts, from one reading of what is left.
	var grants []*grant
	parent := runBudgetFrom(ctx)
	if parent != nil {
		live := 0
		for _, c := range children {
			if c != nil {
				live++
			}
		}
		var err error
		grants, err = parent.grantBatch(live)
		if err != nil {
			for i := range results {
				if children[i] != nil {
					results[i] = models.SubagentResult{Name: children[i].Name, IsError: true, Error: err.Error()}
				}
			}
			return results
		}
		defer func() {
			for _, g := range grants {
				parent.release(g)
			}
		}()
	}

	var wg sync.WaitGroup
	next := 0
	for i := range specs {
		if children[i] == nil {
			continue
		}
		var g *grant
		if grants != nil {
			g = grants[next]
			next++
		}
		wg.Add(1)
		go func(i int, g *grant) {
			defer wg.Done()
			results[i] = h.e.runSubagent(ctx, children[i], resolved[i], specs[i], g)
		}(i, g)
	}
	wg.Wait()
	return results
}

// runSubagent runs a built subagent to completion under grant g (nil when the parent has no
// budget) and reports its result. Whatever happens inside, the parent gets a result back.
func (e *Executor) runSubagent(ctx context.Context, child *models.Agent, resolved *models.ResolvedIngredients, spec models.SubagentSpec, g *grant) models.SubagentResult {
	ctx = contracts.WithoutBudget(ctx) // the request's allowance is the parent's; the child gets a share
	ctx = context.WithValue(ctx, subagentCtxKey{}, true)
	ctx = context.WithValue(ctx, taskModeKey{}, false) // a subagent is not itself a background task
	if g != nil {
		ctx = withGrant(ctx, g)
	}
	resp, trace, err := e.Execute(ctx, child, spec.Input, resolved, nil, false)
	res := models.SubagentResult{Name: child.Name, Output: resp}
	if trace != nil {
		res.TraceID, res.Usage, res.Turns = trace.TraceID, trace.Usage, len(trace.Turns)
	}
	if err != nil {
		res.IsError, res.Error = true, err.Error()
	}
	return res
}

var subagentName = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

// buildSubagent turns a spec into an agent and its ingredients, narrowing what the parent has.
func buildSubagent(parent *models.Agent, pres *models.ResolvedIngredients, spec models.SubagentSpec) (*models.Agent, *models.ResolvedIngredients, error) {
	if strings.TrimSpace(spec.Input) == "" {
		return nil, nil, fmt.Errorf("a subagent needs an input: the task it should do")
	}
	if pres == nil || pres.Model == nil {
		return nil, nil, fmt.Errorf("the parent has no resolved model to give a subagent")
	}

	label := subagentName.ReplaceAllString(strings.TrimSpace(spec.Name), "-")
	if len(label) > 32 {
		label = label[:32]
	}
	if label == "" {
		label = "sub"
	}
	spec.Name = label

	// The pool a subagent may draw tools from: the parent's own, minus anything that needs a
	// person's approval (a subagent cannot pause for one).
	approval := map[string]bool{}
	for _, n := range parent.ApprovalTools {
		approval[n] = true
	}
	pool := map[string]models.ResolvedTool{}
	var order []string
	for _, t := range allResolvedTools(pres) {
		if approval[t.Name] {
			continue
		}
		if _, dup := pool[t.Name]; !dup {
			order = append(order, t.Name)
		}
		pool[t.Name] = t
	}
	var tools []models.ResolvedTool
	for _, name := range spec.Tools {
		if name == "*" {
			tools = tools[:0]
			for _, n := range order {
				tools = append(tools, pool[n])
			}
			break
		}
		t, ok := pool[name]
		if !ok {
			if approval[name] {
				return nil, nil, fmt.Errorf("tool %q needs a person's approval and cannot be given to a subagent", name)
			}
			return nil, nil, fmt.Errorf("tool %q is not one of the parent's tools (available: %s)", name, strings.Join(order, ", "))
		}
		tools = append(tools, t)
	}

	// Skills contribute their instructions; their tools are only available if named in Tools.
	var skills []models.ResolvedSkill
	for _, name := range spec.Skills {
		found := false
		for _, sk := range pres.Skills {
			if sk.Name == name {
				skills = append(skills, models.ResolvedSkill{Name: sk.Name, Instructions: sk.Instructions})
				found = true
			}
		}
		if !found {
			return nil, nil, fmt.Errorf("skill %q is not one of the parent's skills", name)
		}
	}

	model := *pres.Model
	if m := strings.TrimSpace(spec.Model); m != "" {
		model.Model = m
	}

	prompt := strings.TrimSpace(spec.SystemPrompt)
	if prompt == "" {
		prompt = "You are a focused subagent."
	}
	prompt += "\n\nAnother agent started you to do one task. Do it and reply with the result only: your whole conversation is not shared, only your final answer."

	maxTurns := parent.MaxTurns
	if maxTurns <= 0 {
		maxTurns = DefaultMaxTurns
	}
	child := &models.Agent{
		Name:          fmt.Sprintf("%s/%s-%d", parent.Name, label, subagentSeq.Add(1)),
		Kitchen:       parent.Kitchen,
		Mode:          models.AgentModeManaged,
		Status:        models.AgentStatusReady,
		Description:   prompt,
		MaxTurns:      maxTurns,
		Budget:        spec.Budget,
		ContextBudget: parent.ContextBudget,
		SummaryModel:  parent.SummaryModel,
		// The parent's rules bind its subagents; the workspace policy is applied on top by
		// the executor, as it is for every agent.
		Guardrails: parent.Guardrails,
	}
	return child, &models.ResolvedIngredients{Model: &model, Tools: tools, Skills: skills}, nil
}
