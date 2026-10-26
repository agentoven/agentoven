import { Bot, Boxes, GitBranch, GitFork, Hourglass, Search, ShieldCheck, UserCheck, Workflow, type LucideIcon } from 'lucide-react';
import type { StepType, WireStep } from '../../lib/recipeFlow';
import { CARRY_ON } from '../../lib/recipeFlow';

/** How each kind of step looks and what it is called, in plain words. */
export interface StepMeta {
  type: StepType | 'carry' | 'other';
  label: string;
  hint: string;
  icon: LucideIcon;
  color: string;
  bg: string;
}

const AGENT: StepMeta = { type: 'agent', label: 'Agent', hint: 'Hand a task to one of your agents', icon: Bot, color: '#93c5fd', bg: 'rgba(96,165,250,0.18)' };
const LOOKUP: StepMeta = { type: 'lookup', label: 'Look something up', hint: 'Search your knowledge', icon: Search, color: '#c4b5fd', bg: 'rgba(167,139,250,0.18)' };
const PERSON: StepMeta = { type: 'person', label: 'Ask a person', hint: 'Wait for someone to approve', icon: UserCheck, color: '#fcd34d', bg: 'rgba(251,191,36,0.18)' };
const CHECK: StepMeta = { type: 'check', label: 'Check quality', hint: 'Score a result before it moves on', icon: ShieldCheck, color: '#67e8f9', bg: 'rgba(34,211,238,0.16)' };
const DECIDE: StepMeta = { type: 'decide', label: 'Decide', hint: 'Take one way or another', icon: GitBranch, color: '#5eead4', bg: 'rgba(45,212,191,0.16)' };
const PARALLEL: StepMeta = { type: 'parallel', label: 'Do at the same time', hint: 'Start several steps together', icon: GitFork, color: '#fdba74', bg: 'rgba(251,146,60,0.18)' };
const RECIPE: StepMeta = { type: 'recipe', label: 'Run a recipe', hint: 'Use another recipe as one step', icon: Boxes, color: '#f0abfc', bg: 'rgba(232,121,249,0.16)' };
const WAIT: StepMeta = { type: 'wait', label: 'Wait', hint: 'Pause for a while, or until a time', icon: Hourglass, color: '#fda4af', bg: 'rgba(251,113,133,0.15)' };
const CARRY: StepMeta = { type: 'carry', label: 'Carry on', hint: 'Nothing to do on this path', icon: Workflow, color: '#cbd5e1', bg: 'rgba(148,163,184,0.18)' };

export const PALETTE: { title: string; items: StepMeta[] }[] = [
  { title: 'Do the work', items: [AGENT, LOOKUP, RECIPE] },
  { title: 'Choose a path', items: [DECIDE, PARALLEL] },
  { title: 'Keep it safe', items: [PERSON, CHECK] },
  { title: 'Timing', items: [WAIT] },
];

export const META_BY_TYPE: Record<StepType, StepMeta> = { agent: AGENT, lookup: LOOKUP, person: PERSON, check: CHECK, decide: DECIDE, parallel: PARALLEL, recipe: RECIPE, wait: WAIT };

export function metaOfStep(step: WireStep): StepMeta {
  switch (step.kind) {
    case 'agent': return AGENT;
    case 'evaluator': return CHECK;
    case 'rag': return LOOKUP;
    case 'human_gate': return PERSON;
    case 'condition': return DECIDE;
    case 'sub_recipe': return RECIPE;
    case 'wait': return WAIT;
    case 'parallel': return PARALLEL;
    case 'fan_out': return step.config?.ui === CARRY_ON ? CARRY : { ...CARRY, type: 'other', label: step.kind };
    default: return { ...CARRY, type: 'other', label: step.kind };
  }
}
