import { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { BookOpen, Plus, Trash2, Play, GitBranch } from 'lucide-react';
import { recipes, type Recipe } from '../api';
import { useAPI } from '../hooks';
import {
  PageHeader, Card, EmptyState,
  Spinner, ErrorBanner, Button,
} from '../components/UI';

export function RecipesPage() {
  const { data, loading, error, refetch } = useAPI(recipes.list);
  const [showForm, setShowForm] = useState(false);

  return (
    <div>
      <PageHeader
        title="Recipes"
        description="Multi-agent workflows (DAGs)"
        action={
          <Button onClick={() => setShowForm(!showForm)}>
            <Plus size={16} className="mr-1.5" /> Create Recipe
          </Button>
        }
      />

      {error && <ErrorBanner message={error} onRetry={refetch} />}
      {showForm && <RecipeForm onCreated={() => { setShowForm(false); refetch(); }} />}

      {loading ? (
        <Spinner />
      ) : data && data.length > 0 ? (
        <div className="p-8 grid grid-cols-1 md:grid-cols-2 gap-4">
          {data.map((recipe) => (
            <RecipeCard key={recipe.id} recipe={recipe} onAction={refetch} />
          ))}
        </div>
      ) : (
        <EmptyState
          icon={<BookOpen size={48} />}
          title="No recipes yet"
          description="Create a recipe to orchestrate multi-agent workflows."
          action={<Button onClick={() => setShowForm(true)}>Create Recipe</Button>}
        />
      )}
    </div>
  );
}

function RecipeCard({ recipe, onAction }: { recipe: Recipe; onAction: () => void }) {
  const [busy, setBusy] = useState(false);
  const navigate = useNavigate();

  const doAction = async (fn: () => Promise<unknown>) => {
    setBusy(true);
    try { await fn(); onAction(); } catch { /* toast */ }
    setBusy(false);
  };

  return (
    <Card>
      <div className="flex items-start justify-between mb-3">
        <div className="flex items-center gap-2">
          <BookOpen size={18} className="text-[var(--ao-brand-light)]" />
          <span className="font-medium">{recipe.name}</span>
        </div>
        <span className="text-xs text-[var(--ao-text-muted)]">v{recipe.version}</span>
      </div>

      {recipe.description && (
        <p className="text-sm text-[var(--ao-text-muted)] mb-3 line-clamp-2">{recipe.description}</p>
      )}

      <div className="text-xs text-[var(--ao-text-muted)] mb-4">
        <p>{recipe.steps?.length || 0} steps</p>
        {recipe.steps?.map((step, i) => (
          <span
            key={i}
            className="inline-block mt-1 mr-1 px-2 py-0.5 rounded bg-[var(--ao-bg)] border border-[var(--ao-border)]"
          >
            {step.name} ({step.kind})
          </span>
        ))}
      </div>

      <div className="flex gap-2">
        <Button size="sm" onClick={() => doAction(() => recipes.bake(recipe.name, {}))} disabled={busy}>
          <Play size={14} className="mr-1" /> Run
        </Button>
        <Button size="sm" variant="secondary" onClick={() => navigate(`/dishshelf/${recipe.name}`)}>
          <GitBranch size={14} className="mr-1" /> DishShelf
        </Button>
        <Button size="sm" variant="danger" onClick={() => doAction(() => recipes.delete(recipe.name))} disabled={busy}>
          <Trash2 size={14} />
        </Button>
      </div>
    </Card>
  );
}

function RecipeForm({ onCreated }: { onCreated: () => void }) {
  const [form, setForm] = useState({ name: '', description: '' });
  const [submitting, setSubmitting] = useState(false);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!form.name) return;
    setSubmitting(true);
    try {
      await recipes.create({ ...form, steps: [] });
      onCreated();
    } catch { /* toast */ }
    setSubmitting(false);
  };

  return (
    <Card className="mx-8 mt-4">
      <form onSubmit={submit} className="flex flex-wrap gap-3 items-end">
        <div className="flex-1 min-w-[200px]">
          <label className="block text-xs text-[var(--ao-text-muted)] mb-1">Name *</label>
          <input
            value={form.name}
            onChange={(e) => setForm({ ...form, name: e.target.value })}
            className="w-full px-3 py-2 rounded-lg bg-[var(--ao-bg)] border border-[var(--ao-border)] text-sm outline-none focus:border-[var(--ao-brand)]"
            placeholder="my-workflow"
          />
        </div>
        <div className="flex-1 min-w-[200px]">
          <label className="block text-xs text-[var(--ao-text-muted)] mb-1">Description</label>
          <input
            value={form.description}
            onChange={(e) => setForm({ ...form, description: e.target.value })}
            className="w-full px-3 py-2 rounded-lg bg-[var(--ao-bg)] border border-[var(--ao-border)] text-sm outline-none focus:border-[var(--ao-brand)]"
            placeholder="A workflow that..."
          />
        </div>
        <Button disabled={submitting || !form.name}>
          {submitting ? 'Creating...' : 'Create'}
        </Button>
      </form>
    </Card>
  );
}
