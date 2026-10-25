import { useEffect, useMemo, useState } from 'react';
import { Link, NavLink, Outlet, useLocation } from 'react-router-dom';
import { ChevronsLeft, PanelLeft, Search, X } from 'lucide-react';
import { useLocalStorageState } from '../hooks';
import { NAV_SECTIONS, itemMatches, sectionMatches, type NavItem, type NavSection } from '../nav';

const NARROW_QUERY = '(max-width: 899px)';

/** True while the viewport is narrower than 900px. */
function useNarrowScreen(): boolean {
  const [narrow, setNarrow] = useState(() => typeof window !== 'undefined' && window.matchMedia(NARROW_QUERY).matches);
  useEffect(() => {
    const mq = window.matchMedia(NARROW_QUERY);
    const onChange = () => setNarrow(mq.matches);
    onChange();
    mq.addEventListener('change', onChange);
    return () => mq.removeEventListener('change', onChange);
  }, []);
  return narrow;
}

export function Layout() {
  const location = useLocation();
  // The panel can be tucked away on a wide screen. On a narrow one it is an overlay that a tap on the rail opens.
  const [panelHidden, setPanelHidden] = useLocalStorageState<boolean>('ao-oss-sidebar-collapsed', false);
  // The overlay belongs to the page it was opened on, so moving to another page closes it.
  const [overlayPath, setOverlayPath] = useState<string | null>(null);
  const overlayOpen = overlayPath === location.pathname;
  const setOverlayOpen = (open: boolean) => setOverlayPath(open ? location.pathname : null);
  const [search, setSearch] = useState('');
  const narrow = useNarrowScreen();

  const active: NavSection = NAV_SECTIONS.find((s) => sectionMatches(s, location.pathname)) ?? NAV_SECTIONS[0];
  const firstPath = (s: NavSection) => s.groups[0].items[0].to;

  const query = search.trim().toLowerCase();
  const results = useMemo(() => {
    if (!query) return [];
    return NAV_SECTIONS.flatMap((s) => s.groups.flatMap((g) => g.items.filter((i) => i.label.toLowerCase().includes(query)).map((i) => ({ item: i, section: s.label }))));
  }, [query]);

  const panelVisible = narrow ? overlayOpen : !panelHidden;

  const renderItem = (item: NavItem) => {
    const Icon = item.icon;
    const isActive = itemMatches(item, location.pathname);
    return (
      <NavLink
        key={item.to}
        to={item.to}
        end={item.to === '/'}
        aria-current={isActive ? 'page' : undefined}
        className={`flex items-center gap-2.5 h-[38px] px-2.5 rounded-lg text-sm font-medium transition-colors ${
          isActive ? 'bg-[var(--ao-surface-hover)] text-[var(--ao-text)]' : 'text-slate-300 hover:bg-[var(--ao-surface-hover)]/60'
        }`}
      >
        <Icon size={18} className={`shrink-0 ${isActive ? 'text-[var(--ao-brand-light)]' : 'text-[var(--ao-text-muted)]'}`} />
        {item.label}
      </NavLink>
    );
  };

  return (
    <div className="flex h-screen">
      {/* Rail: the sections */}
      <nav aria-label="Sections" className="w-16 shrink-0 z-30 flex flex-col items-center gap-1 py-3 bg-[var(--ao-bg)] border-r border-[var(--ao-border)]">
        <Link to="/" aria-label="AgentOven home" className="mb-2.5">
          <img src="/logo-mark.png" alt="AgentOven" className="w-12 h-12 object-contain block" />
        </Link>
        {NAV_SECTIONS.map((s) => {
          const Icon = s.icon;
          const isActive = s.id === active.id;
          return (
            <Link
              key={s.id}
              to={isActive && narrow ? location.pathname : firstPath(s)}
              aria-label={s.label}
              aria-current={isActive ? 'true' : undefined}
              onClick={() => { if (narrow) setOverlayOpen(true); }}
              className={`w-[52px] h-[54px] rounded-[10px] flex flex-col items-center justify-center gap-1 ${
                isActive ? 'text-[var(--ao-brand-light)] bg-[rgba(230,81,0,0.16)]' : 'text-[var(--ao-text-muted)] hover:text-[var(--ao-text)]'
              }`}
            >
              <Icon size={20} />
              <span className="text-[11px] font-medium">{s.label}</span>
            </Link>
          );
        })}
        <div className="flex-1" />
        {!narrow && panelHidden && (
          <button
            type="button"
            onClick={() => setPanelHidden(false)}
            aria-label="Show menu"
            title="Show menu"
            className="w-10 h-10 grid place-items-center rounded-lg border border-[var(--ao-border)] text-[var(--ao-text-muted)] hover:text-[var(--ao-text)]"
          >
            <PanelLeft size={16} />
          </button>
        )}
      </nav>

      {narrow && overlayOpen && (
        <div onClick={() => setOverlayOpen(false)} aria-hidden="true" className="fixed inset-0 z-10 bg-[rgba(15,23,42,0.6)]" />
      )}

      {/* Panel: the selected section's items */}
      {panelVisible && (
        <aside
          aria-label={`${active.label} menu`}
          className={`w-[232px] shrink-0 flex flex-col gap-3.5 px-3 py-3.5 bg-[var(--ao-surface)] border-r border-[var(--ao-border)] ${
            narrow ? 'fixed top-0 bottom-0 left-16 z-20' : ''
          }`}
        >
          <div className="flex items-center gap-2 h-10 px-1">
            <span className="text-[1.05rem] font-bold text-[var(--ao-text)]">AgentOven</span>
            {narrow && (
              <button type="button" onClick={() => setOverlayOpen(false)} aria-label="Close menu" className="ml-auto text-[var(--ao-text-muted)]">
                <X size={16} />
              </button>
            )}
          </div>

          <label className="relative block">
            <span className="sr-only">Jump to a page</span>
            <Search size={14} className="absolute left-2.5 top-[11px] text-[var(--ao-text-muted)]" />
            <input
              type="text"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder="Jump to…"
              className="w-full h-9 pl-[30px] pr-2.5 rounded-lg text-sm bg-transparent border border-[var(--ao-border)] text-[var(--ao-text)] placeholder:text-[var(--ao-text-muted)] outline-none focus:border-[var(--ao-brand)]"
            />
          </label>

          <div className="flex-1 min-h-0 overflow-y-auto">
            {query ? (
              results.length > 0 ? (
                results.map(({ item, section }) => (
                  <div key={item.to}>
                    {renderItem(item)}
                    <div className="-mt-1 mb-1 ml-[38px] text-[11px] text-[var(--ao-text-muted)]">{section}</div>
                  </div>
                ))
              ) : (
                <p className="px-2.5 py-2 text-sm text-[var(--ao-text-muted)]">No pages match "{search}"</p>
              )
            ) : (
              active.groups.map((g, i) => (
                <div key={g.label ?? i} className={i === 0 ? '' : 'pt-3.5'}>
                  {g.label && (
                    <div className="px-2.5 pb-1 text-xs font-semibold uppercase tracking-wider text-[var(--ao-text-muted)]">{g.label}</div>
                  )}
                  <div className="flex flex-col gap-0.5">{g.items.map(renderItem)}</div>
                </div>
              ))
            )}
          </div>

          <div className="border-t border-[var(--ao-border)] pt-3 text-xs text-[var(--ao-text-muted)]">
            <div className="px-1.5">AgentOven OSS · Community</div>
            {!narrow && (
              <button
                type="button"
                onClick={() => setPanelHidden(true)}
                aria-label="Hide menu"
                className="mt-2.5 px-1.5 flex items-center gap-1.5 hover:text-[var(--ao-text)]"
              >
                <ChevronsLeft size={14} /> Hide menu
              </button>
            )}
          </div>
        </aside>
      )}

      {/* Main content */}
      <main className="flex-1 min-w-0 overflow-auto">
        <Outlet />
      </main>
    </div>
  );
}
