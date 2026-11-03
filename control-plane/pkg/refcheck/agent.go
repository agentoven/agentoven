package refcheck

import (
	"fmt"
	"strings"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// Limits of the numeric settings. They are generous: they exist to refuse a value that cannot be meant
// (a negative number, a million turns), not to ration anything.
const (
	maxTurnsLimit      = 1000
	minContextBudget   = 256
	maxContextBudget   = 10_000_000
	maxNameLength      = 128
	maxTopK            = 1000
	maxEmbeddingDims   = 65536
	maxEmbeddingBatch  = 100_000
	maxScenarioRollout = 1000
)

var (
	agentModes      = []string{string(models.AgentModeManaged), string(models.AgentModeExternal)}
	agentRuntimes   = []string{string(models.RuntimeAgentOven), string(models.RuntimeLangChain), string(models.RuntimeLangGraph), string(models.RuntimeDeepAgents), string(models.RuntimeCrewAI), string(models.RuntimeCustom), string(models.RuntimeAgenticBot)}
	execModes       = []string{string(models.ExecModeLocal), string(models.ExecModeDocker), string(models.ExecModeK8s)}
	behaviors       = []string{string(models.BehaviorReactive), string(models.BehaviorAgentic)}
	strategies      = []string{string(models.StrategyReAct), string(models.StrategyPlanAndExecute), string(models.StrategyReflexion)}
	ingredientKinds = []string{
		string(models.IngredientModel), string(models.IngredientTool), string(models.IngredientPrompt), string(models.IngredientData),
		string(models.IngredientObservability), string(models.IngredientEmbedding), string(models.IngredientVectorStore),
		string(models.IngredientRetriever), string(models.IngredientScenario), string(models.IngredientSkill),
	}
	vectorBackends = []string{
		string(models.VectorStoreEmbedded), string(models.VectorStorePGVector), string(models.VectorStorePinecone), string(models.VectorStoreQdrant),
		string(models.VectorStoreCosmosDB), string(models.VectorStoreChroma), string(models.VectorStoreSnowflake), string(models.VectorStoreDatabricks),
	}
	guardrailKinds = []string{
		string(models.GuardrailContentFilter), string(models.GuardrailPIIDetection), string(models.GuardrailTopicRestriction), string(models.GuardrailMaxLength),
		string(models.GuardrailRegexFilter), string(models.GuardrailPromptInjection), string(models.GuardrailLlamaGuard), string(models.GuardrailCustom),
	}
	guardrailStages = []string{
		string(models.GuardrailStageInput), string(models.GuardrailStageOutput), string(models.GuardrailStageBoth), string(models.GuardrailStageRetrieval), string(models.GuardrailStageAll),
	}
	guardrailActions = []string{
		string(models.GuardrailActionBlock), string(models.GuardrailActionRedact), string(models.GuardrailActionMask), string(models.GuardrailActionWarn), string(models.GuardrailActionRequireApproval),
	}
)

// CheckAgent checks an agent's configuration against what the kitchen has. The agent is judged as it is
// given: pass the agent with its draft applied (Agent.WithDraft) to judge what the next bake would run.
func CheckAgent(a *models.Agent, c *Catalog, o Options) []Problem {
	ck := &checker{c: c, o: o}
	ck.agent(a)
	return ck.out
}

func (ck *checker) agent(a *models.Agent) {
	if a.Name != "" {
		switch {
		case len(a.Name) > maxNameLength:
			ck.invalid("name", a.Name, fmt.Sprintf("is longer than %d characters", maxNameLength), nil)
		case strings.ContainsAny(a.Name, "/\\?#%") || hasControl(a.Name):
			ck.invalid("name", a.Name, `must not contain "/", "\", "?", "#", "%" or control characters: it is part of the agent's URL`, nil)
		}
	}
	ck.enum("mode", string(a.Mode), agentModes)
	ck.enum("runtime", string(a.Runtime), agentRuntimes)
	ck.enum("execution_mode", string(a.ExecutionMode), execModes)
	ck.enum("behavior", string(a.Behavior), behaviors)
	ck.enum("reasoning_strategy", string(a.ReasoningStrategy), strategies)
	ck.rangeInt("max_turns", int64(a.MaxTurns), 0, maxTurnsLimit)
	if a.ContextBudget != 0 {
		ck.rangeInt("context_budget", int64(a.ContextBudget), minContextBudget, maxContextBudget)
	}

	// The model the agent runs on, the model it falls back to, the model that summarizes its history.
	ck.modelPair("model_provider", "model_name", a.ModelProvider, a.ModelName, true, true)
	ck.modelPair("backup_provider", "backup_model", a.BackupProvider, a.BackupModel, true, true)
	ck.modelPair("summary_provider", "summary_model", a.SummaryProvider, a.SummaryModel, false, true)

	for i, name := range a.Skills {
		ck.skill(fmt.Sprintf("skills[%d]", i), name, false)
	}
	ck.dupes("skills", a.Skills)
	for i, name := range a.ApprovalTools {
		ck.tool(fmt.Sprintf("approval_tools[%d]", i), name, "tool", false)
	}
	ck.dupes("approval_tools", a.ApprovalTools)
	for i, name := range a.Subagents {
		path := fmt.Sprintf("subagents[%d]", i)
		if name == a.Name && a.Name != "" {
			ck.invalid(path, name, "an agent cannot delegate to itself", nil)
			continue
		}
		ck.agentRef(path, name)
	}
	ck.dupes("subagents", a.Subagents)

	if b := a.Budget; b != nil {
		if b.MaxCostUSD < 0 {
			ck.outOfRange("budget.max_cost_usd", b.MaxCostUSD, 0, 1e9, false)
		}
		for _, f := range []struct {
			name string
			v    int64
		}{{"budget.max_tokens", b.MaxTokens}, {"budget.max_active_seconds", b.MaxActiveSeconds}, {"budget.max_turns", int64(b.MaxTurns)}} {
			if f.v < 0 {
				ck.outOfRange(f.name, float64(f.v), 0, 1e12, true)
			}
		}
		if b.ChildShare < 0 || b.ChildShare > 1 {
			ck.outOfRange("budget.child_share", b.ChildShare, 0, 1, false)
		}
	}
	if a.Bot != nil {
		for i, name := range a.Bot.Tools {
			ck.tool(fmt.Sprintf("bot.tools[%d]", i), name, "tool", false)
		}
		ck.dupes("bot.tools", a.Bot.Tools)
	}

	for i, g := range a.Guardrails {
		if !g.Enabled {
			continue // a rule that is switched off is never evaluated, whatever it says
		}
		path := fmt.Sprintf("guardrails[%d]", i)
		ck.enum(path+".kind", string(g.Kind), guardrailKinds)
		ck.enum(path+".stage", string(g.Stage), guardrailStages)
		ck.enum(path+".action", string(g.Action), guardrailActions)
	}

	ck.ingredients(a)

	// A managed agent that is about to run needs a model somewhere.
	if ck.o.Bake && a.IsManaged() && !hasModel(a) {
		ck.add(Problem{Path: "model_provider", Code: CodeRequired, Kind: KindModel,
			Message: "model_provider: a managed agent needs a model: set model_provider and model_name, or add a model ingredient"})
	}
}

func hasModel(a *models.Agent) bool {
	if a.ModelProvider != "" {
		return true
	}
	for _, ing := range a.Ingredients {
		if ing.Kind == models.IngredientModel {
			return true
		}
	}
	return false
}

func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// ingredients checks every ingredient and how they fit together.
func (ck *checker) ingredients(a *models.Agent) {
	seen := map[string]bool{}
	kindCount := map[models.IngredientKind]int{}
	var embeddings []embeddingFacts
	var vectorstores []vectorFacts
	type retr struct {
		path string
		cfg  map[string]interface{}
	}
	var retrievers []retr

	for i, ing := range a.Ingredients {
		path := fmt.Sprintf("ingredients[%d]", i)
		if !contains(ingredientKinds, string(ing.Kind)) {
			if ing.Kind == "" {
				ck.required(path+".kind", "an ingredient needs a kind")
			} else {
				ck.notFound(path+".kind", KindIngredientKind, string(ing.Kind), "", ingredientKinds)
			}
			continue
		}
		kindCount[ing.Kind]++
		if ing.Name != "" {
			key := string(ing.Kind) + "\x00" + ing.Name
			if seen[key] {
				ck.add(Problem{Path: path + ".name", Code: CodeDuplicate, Value: ing.Name,
					Message: fmt.Sprintf("%s.name: there is already a %s ingredient named %s", path, ing.Kind, quote(ing.Name))})
			}
			seen[key] = true
		}
		if (ing.Kind == models.IngredientModel || ing.Kind == models.IngredientPrompt) && kindCount[ing.Kind] == 2 {
			ck.add(Problem{Path: path + ".kind", Code: CodeDuplicate, Value: string(ing.Kind),
				Message: fmt.Sprintf("%s.kind: an agent has one %s ingredient; this is the second", path, ing.Kind)})
		}
		cfg := ing.Config
		if cfg == nil {
			cfg = map[string]interface{}{}
		}
		switch ing.Kind {
		case models.IngredientModel:
			ck.modelIngredient(path, cfg)
		case models.IngredientTool:
			name := ing.Name
			if n, ok := ck.cfgString(path+".config", cfg, "tool_name"); ok && n != "" {
				name = n
			}
			if name == "" {
				ck.required(path+".name", "a tool ingredient needs the name of a tool")
				break
			}
			ck.tool(toolPath(path, cfg), name, "tool", ing.Required)
		case models.IngredientObservability:
			name := ing.Name
			if n, ok := ck.cfgString(path+".config", cfg, "tool_name"); ok && n != "" {
				name = n
			}
			if name != "" {
				ck.tool(toolPath(path, cfg), name, "tool", false)
			}
		case models.IngredientPrompt:
			ck.promptIngredient(path, ing, cfg)
		case models.IngredientData:
			uri, isString := ck.cfgString(path+".config", cfg, "uri")
			if wrongType := cfg["uri"] != nil && !isString; !wrongType && strings.TrimSpace(uri) == "" {
				ck.required(path+".config.uri", "a data ingredient needs a uri")
			}
		case models.IngredientEmbedding:
			embeddings = append(embeddings, ck.embeddingIngredient(path, cfg))
		case models.IngredientVectorStore:
			vectorstores = append(vectorstores, ck.vectorStoreIngredient(path, cfg))
		case models.IngredientRetriever:
			ck.retrieverIngredient(path, cfg)
			retrievers = append(retrievers, retr{path, cfg})
		case models.IngredientScenario:
			ck.scenarioIngredient(path, ing, cfg)
		case models.IngredientSkill:
			if ing.Name == "" {
				ck.required(path+".name", "a skill ingredient needs the name of a skill")
				break
			}
			ck.skill(path+".name", ing.Name, ing.Required)
		}
	}

	// A built-in retriever names an embedding and a vector store among the agent's own ingredients.
	for _, r := range retrievers {
		if p := str(r.cfg, "provider"); p != "" && p != "built-in" {
			continue // an external RAG provider keeps its own
		}
		if ref := str(r.cfg, "embedding_ref"); ref != "" {
			found := false
			var names []string
			for _, e := range embeddings {
				names = append(names, e.providerName)
				if e.providerName == ref || e.kind+"/"+e.model == ref || e.kind == ref {
					found = true
				}
			}
			if !found {
				ck.notFound(r.path+".config.embedding_ref", KindEmbedding, ref, "", sorted(names))
			}
		}
		if ref := str(r.cfg, "vectorstore_ref"); ref != "" {
			found := false
			var names []string
			for _, v := range vectorstores {
				names = append(names, v.backend, v.index)
				if v.backend == ref || v.index == ref {
					found = true
				}
			}
			if !found {
				ck.notFound(r.path+".config.vectorstore_ref", KindVectorStore, ref, "", sorted(uniq(names)))
			}
		}
	}
}

func toolPath(path string, cfg map[string]interface{}) string {
	if n, ok := cfg["tool_name"].(string); ok && n != "" {
		return path + ".config.tool_name"
	}
	return path + ".name"
}

func uniq(ss []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range ss {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func (ck *checker) modelIngredient(path string, cfg map[string]interface{}) {
	provider, _ := ck.cfgString(path+".config", cfg, "provider")
	model, _ := ck.cfgString(path+".config", cfg, "model")
	cpath := path + ".config"
	if provider == "" {
		ck.add(Problem{Path: cpath + ".provider", Code: CodeRequired, Severity: ck.gapSeverity(), Message: cpath + ".provider: a model ingredient needs the provider that serves it"})
		return
	}
	p, ok := ck.provider(cpath+".provider", provider)
	if !ok {
		return
	}
	if model == "" {
		ck.add(Problem{Path: cpath + ".model", Code: CodeRequired, Severity: ck.gapSeverity(), Within: provider, Message: fmt.Sprintf("%s.model: provider %s is set and needs a model", cpath, quote(provider))})
		return
	}
	ck.model(cpath+".model", p, model)
	ck.cfgFloat(cpath, cfg, "temperature", 0, 2)
	ck.cfgInt(cpath, cfg, "max_tokens", 1, 10_000_000)
}

func (ck *checker) promptIngredient(path string, ing models.Ingredient, cfg map[string]interface{}) {
	text, _ := ck.cfgString(path+".config", cfg, "text")
	tmpl, _ := ck.cfgString(path+".config", cfg, "template")
	if text != "" || tmpl != "" {
		return // the prompt is written here, there is nothing to look up
	}
	name := ing.Name
	if n, ok := ck.cfgString(path+".config", cfg, "prompt_name"); ok && n != "" {
		name = n
	}
	field := path + ".name"
	if _, ok := cfg["prompt_name"]; ok {
		field = path + ".config.prompt_name"
	}
	if name == "" {
		ck.required(path+".name", "a prompt ingredient needs the prompt's name, or the prompt written in config.text")
		return
	}
	latest, ok := ck.c.Prompts[name]
	if !ok {
		ck.notFound(field, KindPrompt, name, "", ck.c.promptNames())
		return
	}
	if v, present := cfg["version"]; present && v != nil {
		f, isNum := num(v)
		switch {
		case !isNum || f != float64(int64(f)):
			ck.wrongType(path+".config.version", "a whole number", v)
		case int64(f) < 1 || int64(f) > int64(latest):
			ck.add(Problem{Path: path + ".config.version", Code: CodeNotFound, Kind: KindPrompt, Value: trimFloat(f), Within: name,
				Message: fmt.Sprintf("%s.config.version: prompt %s has versions 1 to %d, not %s", path, quote(name), latest, trimFloat(f))})
		}
	}
}

type embeddingFacts struct{ providerName, kind, model string }

func (ck *checker) embeddingIngredient(path string, cfg map[string]interface{}) embeddingFacts {
	cpath := path + ".config"
	provider, _ := ck.cfgString(cpath, cfg, "provider")
	model, _ := ck.cfgString(cpath, cfg, "model")
	facts := embeddingFacts{providerName: provider, model: model}
	ck.cfgInt(cpath, cfg, "dimensions", 1, maxEmbeddingDims)
	ck.cfgInt(cpath, cfg, "batch_size", 1, maxEmbeddingBatch)
	if provider == "" {
		ck.add(Problem{Path: cpath + ".provider", Code: CodeRequired, Message: cpath + ".provider: an embedding ingredient needs the provider that serves it"})
		return facts
	}
	if p, ok := ck.provider(cpath+".provider", provider); ok {
		facts.kind = p.Kind
	}
	return facts
}

type vectorFacts struct{ backend, index string }

func (ck *checker) vectorStoreIngredient(path string, cfg map[string]interface{}) vectorFacts {
	cpath := path + ".config"
	backend, _ := ck.cfgString(cpath, cfg, "backend")
	index, _ := ck.cfgString(cpath, cfg, "index")
	ck.cfgInt(cpath, cfg, "dimensions", 1, maxEmbeddingDims)
	if backend == "" {
		backend = string(models.VectorStoreEmbedded)
	}
	if !contains(vectorBackends, backend) {
		ck.notFound(cpath+".backend", KindBackend, backend, "", vectorBackends)
	}
	if index == "" && backend != string(models.VectorStoreEmbedded) {
		ck.required(cpath+".index", "a vector store other than the embedded one needs an index")
	}
	if index == "" {
		index = "default"
	}
	return vectorFacts{backend, index}
}

func (ck *checker) retrieverIngredient(path string, cfg map[string]interface{}) {
	cpath := path + ".config"
	ck.cfgInt(cpath, cfg, "top_k", 1, maxTopK)
	ck.cfgFloat(cpath, cfg, "score_threshold", 0, 1)
	if v, ok := cfg["hybrid_search"]; ok && v != nil {
		if _, isBool := v.(bool); !isBool {
			ck.wrongType(cpath+".hybrid_search", "true or false", v)
		}
	}
}

func (ck *checker) scenarioIngredient(path string, ing models.Ingredient, cfg map[string]interface{}) {
	cpath := path + ".config"
	scenario := ing.Name
	if s, ok := ck.cfgString(cpath, cfg, "scenario"); ok && s != "" {
		scenario = s
	}
	ck.cfgInt(cpath, cfg, "rollouts", 1, maxScenarioRollout)
	ck.cfgFloat(cpath, cfg, "min_pass_rate", 0, 1)
	if scenario == "" {
		ck.required(cpath+".scenario", "a scenario ingredient needs the id of a scenario")
		return
	}
	if ck.c.Scenarios.Known && !contains(ck.c.Scenarios.Names, scenario) {
		field := path + ".name"
		if _, ok := cfg["scenario"]; ok {
			field = cpath + ".scenario"
		}
		ck.notFound(field, KindScenario, scenario, "", sorted(ck.c.Scenarios.Names))
	}
}
