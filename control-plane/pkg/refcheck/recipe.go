package refcheck

import (
	"fmt"
	"strings"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

const (
	maxStepRetries    = 20
	maxStepTimeout    = 7 * 24 * 3600
	maxStepIterations = 10_000
	maxStepConcurrent = 1000
)

var stepKinds = []string{
	string(models.StepAgent), string(models.StepHumanGate), string(models.StepEvaluator), string(models.StepCondition),
	string(models.StepFanOut), string(models.StepFanIn), string(models.StepRAG), string(models.StepRouter),
	string(models.StepMap), string(models.StepSubRecipe), string(models.StepWait), string(models.StepNotify),
}

// CheckRecipe checks a recipe against what the kitchen has: the agents its steps run, the steps they wait
// for, the recipes it nests, the channels it notifies, the credentials it signs in with, the roles that may
// approve its gates, the environment it runs in.
func CheckRecipe(r *models.Recipe, c *Catalog, o Options) []Problem {
	ck := &checker{c: c, o: o}
	ck.recipe(r)
	return ck.out
}

func (ck *checker) recipe(r *models.Recipe) {
	if r.DefaultEnvironment != "" {
		ck.fromSet("default_environment", KindEnvironment, r.DefaultEnvironment, ck.c.Environments)
	}
	if ck.o.Environment != "" {
		ck.fromSet("environment", KindEnvironment, ck.o.Environment, ck.c.Environments)
	}

	names := make([]string, 0, len(r.Steps))
	index := map[string]int{}
	for i, s := range r.Steps {
		path := fmt.Sprintf("steps[%d]", i)
		switch {
		case s.Name == "":
			ck.required(path+".name", "a step needs a name")
		default:
			if _, dup := index[s.Name]; dup {
				ck.add(Problem{Path: path + ".name", Code: CodeDuplicate, Value: s.Name, Message: fmt.Sprintf("%s.name: there is already a step named %s", path, quote(s.Name))})
			} else {
				index[s.Name] = i
				names = append(names, s.Name)
			}
		}
	}
	sortedNames := sorted(names)

	for i := range r.Steps {
		ck.step(fmt.Sprintf("steps[%d]", i), &r.Steps[i], r.Name, sortedNames)
	}
	ck.cycles(r.Steps, index)
}

func (ck *checker) step(path string, s *models.Step, recipeName string, stepNames []string) {
	if s.Kind == "" {
		ck.required(path+".kind", "a step needs a kind")
	} else if !contains(stepKinds, string(s.Kind)) {
		ck.notFound(path+".kind", KindStepKind, string(s.Kind), "", stepKinds)
	}

	// The agent a step runs. "agent" is the client-facing alias of "agent_ref": when both are given they
	// must agree, or one of them is the typo.
	ref := s.AgentRef
	if s.Agent != "" && s.AgentRef != "" && s.Agent != s.AgentRef {
		ck.invalid(path+".agent", s.Agent, fmt.Sprintf("disagrees with agent_ref %s", quote(s.AgentRef)), nil)
	}
	if ref == "" {
		ref = s.Agent
	}
	refPath := path + ".agent_ref"
	if s.AgentRef == "" {
		refPath = path + ".agent"
	}
	switch {
	case ref != "":
		ck.agentRef(refPath, ref)
	case s.Kind == models.StepAgent || s.Kind == models.StepMap:
		ck.required(path+".agent_ref", fmt.Sprintf("a %s step needs the agent to run", s.Kind))
	}

	if s.Kind == models.StepSubRecipe {
		switch {
		case s.RecipeRef == "":
			ck.required(path+".recipe_ref", "a sub_recipe step needs the recipe to run")
		case s.RecipeRef == recipeName && recipeName != "":
			// A recipe nesting itself exists once it is saved, and the server refuses the loop in
			// checkSubRecipeRefs with the path it makes; it is not "missing" here.
		default:
			ck.fromSet(path+".recipe_ref", KindRecipe, s.RecipeRef, ck.c.Recipes)
		}
	}
	if s.Kind == models.StepMap && s.SourcePath == "" {
		ck.required(path+".source_path", "a map step needs the path of the list it runs over")
	}

	for j, dep := range s.DependsOn {
		p := fmt.Sprintf("%s.depends_on[%d]", path, j)
		switch {
		case dep == s.Name && s.Name != "":
			ck.invalid(p, dep, "a step cannot wait for itself", nil)
		case !contains(stepNames, dep):
			ck.notFound(p, KindStep, dep, "", stepNames)
		}
	}
	ck.dupes(path+".depends_on", s.DependsOn)
	for j, b := range s.Branches {
		if b.NextStep != "" && !contains(stepNames, b.NextStep) {
			ck.notFound(fmt.Sprintf("%s.branches[%d].next_step", path, j), KindStep, b.NextStep, "", stepNames)
		}
	}
	if s.DefaultNext != "" && !contains(stepNames, s.DefaultNext) {
		ck.notFound(path+".default_next", KindStep, s.DefaultNext, "", stepNames)
	}
	if qf := str(s.Config, "question_from"); qf != "" && !contains(stepNames, qf) {
		ck.notFound(path+".config.question_from", KindStep, qf, "", stepNames)
	}

	ck.rangeInt(path+".max_retries", int64(s.MaxRetries), 0, maxStepRetries)
	ck.rangeInt(path+".timeout_secs", int64(s.TimeoutSecs), 0, maxStepTimeout)
	ck.rangeInt(path+".max_iterations", int64(s.MaxIterations), 0, maxStepIterations)
	ck.rangeInt(path+".max_concurrency", int64(s.MaxConcurrency), 0, maxStepConcurrent)
	if s.LoopCondition != "" && s.MaxIterations <= 0 {
		ck.required(path+".max_iterations", "a loop_condition needs max_iterations, the hard cap on the loop")
	}

	// Who is told, and who may decide.
	for j, t := range s.NotifyTools {
		ck.tool(fmt.Sprintf("%s.notify_tools[%d]", path, j), t, "notify", true)
	}
	ck.dupes(path+".notify_tools", s.NotifyTools)
	if s.Kind == models.StepNotify {
		ck.channels(path, s)
	}
	if s.AuthKeyRef != "" {
		ck.fromSet(path+".auth_key_ref", KindCredential, s.AuthKeyRef, ck.c.Credentials)
	}
	for j, role := range s.ApproverRoles {
		if ck.c.Roles.Known && !containsFold(ck.c.Roles.Names, role) && !strings.EqualFold(role, "admin") {
			ck.notFound(fmt.Sprintf("%s.approver_roles[%d]", path, j), KindRole, role, "", sorted(ck.c.Roles.Names))
		}
	}
	roles := make([]string, len(s.ApproverRoles))
	for j, r := range s.ApproverRoles {
		roles[j] = strings.ToLower(strings.TrimSpace(r)) // a role is the same role in any case
	}
	ck.dupes(path+".approver_roles", roles)
	for j, e := range s.ApproverEmails {
		if at := strings.Index(e, "@"); at < 1 || at == len(e)-1 || strings.ContainsAny(e, " ,;") {
			ck.invalid(fmt.Sprintf("%s.approver_emails[%d]", path, j), e, "is not an e-mail address", nil)
		}
	}
	ck.dupes(path+".approver_emails", s.ApproverEmails)
	if strings.Contains(s.ApproverDomain, "@") {
		ck.invalid(path+".approver_domain", s.ApproverDomain, `is a domain such as "acme.com", without "@"`, nil)
	}
}

// channels checks the channels a notify step sends to (config.channels).
func (ck *checker) channels(path string, s *models.Step) {
	raw, present := s.Config["channels"]
	if !present || raw == nil {
		return
	}
	var list []string
	switch v := raw.(type) {
	case []interface{}:
		for j, c := range v {
			name, ok := c.(string)
			if !ok {
				ck.wrongType(fmt.Sprintf("%s.config.channels[%d]", path, j), "a channel name", c)
				continue
			}
			list = append(list, name)
		}
	case []string:
		list = v
	default:
		ck.wrongType(path+".config.channels", "a list of channel names", raw)
		return
	}
	for j, name := range list {
		if strings.TrimSpace(name) == "" {
			continue
		}
		ck.fromSet(fmt.Sprintf("%s.config.channels[%d]", path, j), KindChannel, strings.TrimSpace(name), ck.c.Channels)
	}
	ck.dupes(path+".config.channels", list)
}

// cycles reports a loop in the steps' dependencies once, at the step where the loop is found.
func (ck *checker) cycles(steps []models.Step, index map[string]int) {
	const (
		unseen = iota
		walking
		done
	)
	state := make([]int, len(steps))
	var stack []string
	reported := false
	var visit func(i int)
	visit = func(i int) {
		if reported {
			return
		}
		state[i] = walking
		stack = append(stack, steps[i].Name)
		for _, dep := range steps[i].DependsOn {
			j, ok := index[dep]
			if !ok || dep == steps[i].Name {
				continue
			}
			if state[j] == walking {
				loop := append([]string(nil), stack...)
				for len(loop) > 0 && loop[0] != dep {
					loop = loop[1:]
				}
				loop = append(loop, dep)
				ck.add(Problem{Path: fmt.Sprintf("steps[%d].depends_on", i), Code: CodeInvalidValue, Value: dep,
					Message: fmt.Sprintf("steps[%d].depends_on: the steps wait for each other in a circle: %s", i, strings.Join(loop, " -> "))})
				reported = true
				return
			}
			if state[j] == unseen {
				visit(j)
			}
		}
		stack = stack[:len(stack)-1]
		state[i] = done
	}
	for i := range steps {
		if state[i] == unseen && steps[i].Name != "" {
			visit(i)
		}
	}
}
