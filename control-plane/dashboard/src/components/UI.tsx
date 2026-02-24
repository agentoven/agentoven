import { useEffect, type ReactNode } from 'react';
import { X as XIcon } from 'lucide-react';

// ── Status Badge ──────────────────────────────────────────────

const statusColors: Record<string, string> = {
  active: 'bg-emerald-500/20 text-emerald-400',
  baked: 'bg-emerald-500/20 text-emerald-400',
  ready: 'bg-emerald-500/20 text-emerald-400',
  running: 'bg-blue-500/20 text-blue-400',
  baking: 'bg-blue-500/20 text-blue-400',
  completed: 'bg-emerald-500/20 text-emerald-400',
  draft: 'bg-slate-500/20 text-slate-400',
  cooled: 'bg-amber-500/20 text-amber-400',
  retired: 'bg-red-500/20 text-red-400',
  failed: 'bg-red-500/20 text-red-400',
  burnt: 'bg-red-500/20 text-red-400',
  success: 'bg-emerald-500/20 text-emerald-400',
  error: 'bg-red-500/20 text-red-400',
};

export function StatusBadge({ status }: { status: string }) {
  const color = statusColors[status] ?? 'bg-slate-500/20 text-slate-400';
  return (
    <span className={`inline-flex items-center px-2.5 py-0.5 rounded-full text-xs font-medium ${color}`}>
      {status}
    </span>
  );
}

// ── Card ──────────────────────────────────────────────────────

export function Card({
  children,
  className = '',
  onClick,
}: {
  children: ReactNode;
  className?: string;
  onClick?: () => void;
}) {
  return (
    <div
      onClick={onClick}
      className={`bg-[var(--ao-surface)] border border-[var(--ao-border)] rounded-xl p-5 ${
        onClick ? 'cursor-pointer hover:border-[var(--ao-brand)] transition-colors' : ''
      } ${className}`}
    >
      {children}
    </div>
  );
}

// ── Stat Card ─────────────────────────────────────────────────

export function StatCard({
  label,
  value,
  icon,
}: {
  label: string;
  value: string | number;
  icon: ReactNode;
}) {
  return (
    <Card>
      <div className="flex items-center justify-between">
        <div>
          <p className="text-sm text-[var(--ao-text-muted)]">{label}</p>
          <p className="text-2xl font-bold mt-1">{value}</p>
        </div>
        <div className="text-[var(--ao-brand-light)]">{icon}</div>
      </div>
    </Card>
  );
}

// ── Page Header ───────────────────────────────────────────────

export function PageHeader({
  title,
  description,
  action,
}: {
  title: string;
  description?: string;
  action?: ReactNode;
}) {
  return (
    <div className="flex items-center justify-between px-8 py-6 border-b border-[var(--ao-border)]">
      <div>
        <h1 className="text-xl font-bold">{title}</h1>
        {description && <p className="text-sm text-[var(--ao-text-muted)] mt-1">{description}</p>}
      </div>
      {action}
    </div>
  );
}

// ── Empty State ───────────────────────────────────────────────

export function EmptyState({
  icon,
  title,
  description,
  action,
}: {
  icon: ReactNode;
  title: string;
  description: string;
  action?: ReactNode;
}) {
  return (
    <div className="flex flex-col items-center justify-center py-16 text-center">
      <div className="text-[var(--ao-text-muted)] mb-4">{icon}</div>
      <h3 className="text-lg font-medium mb-1">{title}</h3>
      <p className="text-sm text-[var(--ao-text-muted)] max-w-sm mb-4">{description}</p>
      {action}
    </div>
  );
}

// ── Loading Spinner ───────────────────────────────────────────

export function Spinner() {
  return (
    <div className="flex items-center justify-center py-16">
      <div className="w-8 h-8 border-2 border-[var(--ao-brand)] border-t-transparent rounded-full animate-spin" />
    </div>
  );
}

// ── Error Banner ──────────────────────────────────────────────

export function ErrorBanner({ message, onRetry }: { message: string; onRetry?: () => void }) {
  return (
    <div className="mx-8 mt-4 p-4 rounded-lg bg-red-500/10 border border-red-500/30 flex items-center justify-between">
      <span className="text-sm text-red-400">{message}</span>
      {onRetry && (
        <button
          onClick={onRetry}
          className="text-xs text-red-400 hover:text-red-300 underline"
        >
          Retry
        </button>
      )}
    </div>
  );
}

// ── Button ────────────────────────────────────────────────────

export function Button({
  children,
  variant = 'primary',
  size = 'md',
  onClick,
  disabled,
}: {
  children: ReactNode;
  variant?: 'primary' | 'secondary' | 'danger';
  size?: 'sm' | 'md';
  onClick?: () => void;
  disabled?: boolean;
}) {
  const base = 'inline-flex items-center justify-center rounded-lg font-medium transition-colors disabled:opacity-50';
  const sizes = { sm: 'px-3 py-1.5 text-xs', md: 'px-4 py-2 text-sm' };
  const variants = {
    primary: 'bg-[var(--ao-brand)] hover:bg-[var(--ao-brand-light)] text-white',
    secondary: 'bg-[var(--ao-surface-hover)] hover:bg-[var(--ao-border)] text-[var(--ao-text)]',
    danger: 'bg-red-600 hover:bg-red-500 text-white',
  };

  return (
    <button
      onClick={onClick}
      disabled={disabled}
      className={`${base} ${sizes[size]} ${variants[variant]}`}
    >
      {children}
    </button>
  );
}

// ── Modal ─────────────────────────────────────────────────────

export function Modal({
  open,
  onClose,
  title,
  children,
  width = 'max-w-2xl',
}: {
  open: boolean;
  onClose: () => void;
  title: string;
  children: ReactNode;
  width?: string;
}) {
  useEffect(() => {
    if (!open) return;
    const handler = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose(); };
    document.addEventListener('keydown', handler);
    return () => document.removeEventListener('keydown', handler);
  }, [open, onClose]);

  if (!open) return null;

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center">
      {/* Backdrop */}
      <div className="absolute inset-0 bg-black/60 backdrop-blur-sm" onClick={onClose} />
      {/* Dialog */}
      <div className={`relative ${width} w-full mx-4 max-h-[85vh] flex flex-col bg-[var(--ao-surface)] border border-[var(--ao-border)] rounded-2xl shadow-2xl`}>
        {/* Header */}
        <div className="flex items-center justify-between px-6 py-4 border-b border-[var(--ao-border)] shrink-0">
          <h2 className="text-lg font-semibold">{title}</h2>
          <button
            onClick={onClose}
            className="p-1 rounded-lg text-[var(--ao-text-muted)] hover:text-[var(--ao-text)] hover:bg-[var(--ao-surface-hover)] transition-colors"
          >
            <XIcon size={18} />
          </button>
        </div>
        {/* Body */}
        <div className="overflow-y-auto px-6 py-4 flex-1">
          {children}
        </div>
      </div>
    </div>
  );
}
