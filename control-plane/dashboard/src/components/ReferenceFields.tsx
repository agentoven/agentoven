import type { Problem } from '../api';
import { useAgentOptions, useChannelOptions, useCredentialOptions, useRecipeOptions, useSkillOptions, useToolOptions } from '../hooks/references';
import { ReferenceMultiSelect, ReferenceSelect } from './ReferenceSelect';

// The fields of a configuration that name something the kitchen has, each already wired to its list. A page
// drops one in and gets the options, their states, the empty state that says where to register the thing,
// and the red chip for a value that names nothing. `problems` are what the server said about the field.

interface Field { label: string; problems?: Problem[]; disabled?: boolean; id?: string }
interface One extends Field { value: string; onChange: (v: string) => void; clearable?: boolean; placeholder?: string }
interface Many extends Field { value: string[]; onChange: (v: string[]) => void; placeholder?: string }

export function SkillsField({ label = 'Skills', value, onChange, problems, disabled, id }: Partial<Many> & Pick<Many, 'value' | 'onChange'>) {
  const { options, loading } = useSkillOptions();
  return <ReferenceMultiSelect id={id} label={label} options={options} loading={loading} value={value} onChange={onChange} placeholder="Add a skill"
    empty={{ what: 'skills', to: '/skills', link: 'Register a skill' }} problems={problems} disabled={disabled} />;
}

/** `need` is what the tool is used for: "tool" to call it, "notify" to send through it. */
export function ToolsField({ label = 'Tools', need = 'tool', value, onChange, problems, disabled, id, placeholder }: Partial<Many> & Pick<Many, 'value' | 'onChange'> & { need?: 'tool' | 'notify' }) {
  const { options, loading } = useToolOptions(need);
  return <ReferenceMultiSelect id={id} label={label} options={options} loading={loading} value={value} onChange={onChange} placeholder={placeholder ?? 'Add a tool'}
    empty={{ what: need === 'notify' ? 'tools that can send a message' : 'tools', to: '/tools', link: 'Add a tool' }} problems={problems} disabled={disabled} />;
}

export function ToolField({ label = 'Tool', need = 'tool', value, onChange, problems, disabled, id, clearable, placeholder }: Partial<One> & Pick<One, 'value' | 'onChange'> & { need?: 'tool' | 'notify' }) {
  const { options, loading } = useToolOptions(need);
  return <ReferenceSelect id={id} label={label} options={options} loading={loading} value={value} onChange={onChange} clearable={clearable} placeholder={placeholder ?? 'Choose a tool'}
    empty={{ what: need === 'notify' ? 'tools that can send a message' : 'tools', to: '/tools', link: 'Add a tool' }} problems={problems} disabled={disabled} />;
}

/** The agents a field may delegate to or run. `self` is left out. */
export function AgentsField({ label = 'Agents', self = '', value, onChange, problems, disabled, id }: Partial<Many> & Pick<Many, 'value' | 'onChange'> & { self?: string }) {
  const { options, loading } = useAgentOptions(self);
  return <ReferenceMultiSelect id={id} label={label} options={options} loading={loading} value={value} onChange={onChange} placeholder="Add an agent"
    empty={{ what: 'other agents', to: '/agents', link: 'Create an agent' }} problems={problems} disabled={disabled} />;
}

export function AgentField({ label = 'Agent', self = '', value, onChange, problems, disabled, id, clearable, placeholder }: Partial<One> & Pick<One, 'value' | 'onChange'> & { self?: string }) {
  const { options, loading } = useAgentOptions(self);
  return <ReferenceSelect id={id} label={label} options={options} loading={loading} value={value} onChange={onChange} clearable={clearable} placeholder={placeholder ?? 'Choose an agent'}
    empty={{ what: 'agents', to: '/agents', link: 'Create an agent' }} problems={problems} disabled={disabled} />;
}

export function RecipeField({ label = 'Recipe', self = '', value, onChange, problems, disabled, id, clearable }: Partial<One> & Pick<One, 'value' | 'onChange'> & { self?: string }) {
  const { options, loading } = useRecipeOptions(self);
  return <ReferenceSelect id={id} label={label} options={options} loading={loading} value={value} onChange={onChange} clearable={clearable} placeholder="Choose a recipe"
    empty={{ what: 'other recipes', to: '/recipes', link: 'Open Recipes' }} problems={problems} disabled={disabled} />;
}

export function ChannelsField({ label = 'Channels', value, onChange, problems, disabled, id }: Partial<Many> & Pick<Many, 'value' | 'onChange'>) {
  const { options, loading } = useChannelOptions();
  return <ReferenceMultiSelect id={id} label={label} options={options} loading={loading} value={value} onChange={onChange} placeholder="Add a channel"
    empty={{ what: 'channels' }} problems={problems} disabled={disabled} />;
}

/** The kitchen's stored credentials, by name: never their values. */
export function CredentialField({ label = 'Credential', value, onChange, problems, disabled, id, clearable = true }: Partial<One> & Pick<One, 'value' | 'onChange'>) {
  const { options, loading } = useCredentialOptions();
  return <ReferenceSelect id={id} label={label} options={options} loading={loading} value={value} onChange={onChange} clearable={clearable} placeholder="Choose a credential"
    empty={{ what: 'credentials', to: '/secrets', link: 'Add a credential' }} problems={problems} disabled={disabled} />;
}

