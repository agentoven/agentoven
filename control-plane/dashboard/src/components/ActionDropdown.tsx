import { useEffect, useRef, useState, type ReactNode } from 'react';
import { createPortal } from 'react-dom';
import { ChevronDown } from 'lucide-react';

export interface MenuItem { label: string; icon: ReactNode; onClick: () => void; danger?: boolean }

/**
 * A secondary button with a dropdown. With `main` it is a split button: the label runs the main action
 * and the caret opens the other actions. Without it the whole button just opens the menu. The menu opens
 * upward when there is no room below, and closes on outside click, scroll or Escape.
 */
export function ActionDropdown({ items, disabled, label, main, className = '' }: {
  items: MenuItem[];
  disabled?: boolean;
  label: string;
  main?: { label: string; icon: ReactNode; onClick: () => void };
  className?: string;
}) {
  const [open, setOpen] = useState(false);
  const [pos, setPos] = useState<{ top?: number; bottom?: number; right: number } | null>(null);
  const ref = useRef<HTMLDivElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);

  // The menu is drawn on the page, not inside the row, so a scrolling table or a card edge never clips it.
  const toggle = () => {
    if (open) { setOpen(false); return; }
    const r = ref.current?.getBoundingClientRect();
    if (!r) return;
    const needed = items.length * 34 + 16;
    const below = window.innerHeight - r.bottom;
    setPos(below < needed && r.top > needed
      ? { bottom: window.innerHeight - r.top + 4, right: window.innerWidth - r.right }
      : { top: r.bottom + 4, right: window.innerWidth - r.right });
    setOpen(true);
  };

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      const t = e.target as Node;
      if (ref.current?.contains(t) || menuRef.current?.contains(t)) return;
      setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') setOpen(false); };
    const close = () => setOpen(false);
    document.addEventListener('mousedown', onDown);
    document.addEventListener('keydown', onKey);
    window.addEventListener('scroll', close, true);
    window.addEventListener('resize', close);
    return () => {
      document.removeEventListener('mousedown', onDown);
      document.removeEventListener('keydown', onKey);
      window.removeEventListener('scroll', close, true);
      window.removeEventListener('resize', close);
    };
  }, [open]);

  const part = 'inline-flex items-center justify-center gap-1 px-3 py-1.5 text-xs font-medium bg-[var(--ao-surface-hover)] hover:bg-[var(--ao-border)] text-[var(--ao-text)] transition-colors cursor-pointer ' +
    'disabled:cursor-not-allowed disabled:text-[var(--ao-text-muted)] disabled:hover:bg-[var(--ao-surface-hover)]';

  return (
    <div ref={ref} className={`relative ${className}`}>
      <div className="flex rounded-lg overflow-hidden">
        {main && (
          <button type="button" onClick={main.onClick} className={`${part} flex-1`}>
            {main.icon} {main.label}
          </button>
        )}
        <button
          type="button"
          aria-label={main ? `More actions for ${label}` : label}
          aria-haspopup="menu"
          aria-expanded={open}
          disabled={disabled}
          onClick={toggle}
          className={`${part} ${main ? '!px-1.5 border-l border-[var(--ao-border)]' : 'flex-1'}`}
        >
          {!main && 'More'}
          <ChevronDown size={14} className={`transition-transform ${open ? 'rotate-180' : ''}`} />
        </button>
      </div>
      {open && pos && createPortal(
        <div
          ref={menuRef}
          role="menu"
          style={{ position: 'fixed', top: pos.top, bottom: pos.bottom, right: pos.right }}
          className="z-50 min-w-[150px] py-1 rounded-lg border border-[var(--ao-border)] bg-[var(--ao-surface)] shadow-xl"
        >
          {items.map((it) => (
            <button
              key={it.label}
              type="button"
              role="menuitem"
              onClick={() => { setOpen(false); it.onClick(); }}
              className={`w-full flex items-center gap-2 px-3 py-1.5 text-sm text-left hover:bg-[var(--ao-surface-hover)] ${it.danger ? 'text-red-400' : ''}`}
            >
              {it.icon} {it.label}
            </button>
          ))}
        </div>,
        document.body,
      )}
    </div>
  );
}
