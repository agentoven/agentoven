import { useEffect, useMemo, useState } from 'react';
import { NavLink, Outlet, useLocation } from 'react-router-dom';
import {
  LayoutDashboard,
  Bot,
  BookOpen,
  Cpu,
  Library,
  Wrench,
  Activity,
  FileText,
  Boxes,
  Database,
  Search,
  Plug,
  GitBranch,
  ChevronDown,
  ChevronRight,
  ChevronsLeft,
  ChevronsRight,
} from 'lucide-react';
import { useLocalStorageState } from '../hooks';

interface NavItem {
  to: string;
  label: string;
  icon: typeof LayoutDashboard;
}

interface NavGroup {
  id: string;
  label: string;
  items: NavItem[];
}

const overviewItem: NavItem = { to: '/', label: 'Overview', icon: LayoutDashboard };

const navGroups: NavGroup[] = [
  {
    id: 'agents',
    label: 'Agents',
    items: [
      { to: '/agents', label: 'Agents', icon: Bot },
      { to: '/recipes', label: 'Recipes', icon: BookOpen },
      { to: '/dishshelf', label: 'DishShelf', icon: GitBranch },
      { to: '/prompts', label: 'Prompts', icon: FileText },
    ],
  },
  {
    id: 'context',
    label: 'Context',
    items: [
      { to: '/catalog', label: 'Model Catalog', icon: Library },
      { to: '/embeddings', label: 'Embeddings', icon: Boxes },
      { to: '/vectorstores', label: 'Vector Stores', icon: Database },
      { to: '/rag', label: 'RAG Pipelines', icon: Search },
      { to: '/connectors', label: 'Connectors', icon: Plug },
    ],
  },
  {
    id: 'providers',
    label: 'Providers',
    items: [
      { to: '/providers', label: 'Providers', icon: Cpu },
      { to: '/tools', label: 'Tools', icon: Wrench },
    ],
  },
  {
    id: 'governance',
    label: 'Governance',
    items: [
      { to: '/traces', label: 'Traces', icon: Activity },
    ],
  },
];

function findGroupIdForPath(pathname: string): string | null {
  for (const group of navGroups) {
    if (group.items.some((item) => pathname.startsWith(item.to))) {
      return group.id;
    }
  }
  return null;
}

export function Layout() {
  const location = useLocation();
  const [expandedGroups, setExpandedGroups] = useLocalStorageState<string[]>(
    'ao-oss-nav-groups',
    navGroups.map((g) => g.id),
  );
  const [collapsed, setCollapsed] = useLocalStorageState<boolean>('ao-oss-sidebar-collapsed', false);
  const [search, setSearch] = useState('');

  // Auto-expand the group containing the active route.
  useEffect(() => {
    const activeGroupId = findGroupIdForPath(location.pathname);
    if (activeGroupId && !expandedGroups.includes(activeGroupId)) {
      setExpandedGroups((prev) => [...prev, activeGroupId]);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [location.pathname]);

  const query = search.trim().toLowerCase();
  const isSearching = query.length > 0;

  const filteredGroups = useMemo(() => {
    if (!isSearching) return navGroups;
    return navGroups
      .map((group) => ({
        ...group,
        items: group.items.filter((item) => item.label.toLowerCase().includes(query)),
      }))
      .filter((group) => group.items.length > 0);
  }, [isSearching, query]);

  const showOverview = !isSearching || overviewItem.label.toLowerCase().includes(query);

  function toggleGroup(id: string) {
    setExpandedGroups((prev) =>
      prev.includes(id) ? prev.filter((g) => g !== id) : [...prev, id],
    );
  }

  function navLinkClass({ isActive }: { isActive: boolean }) {
    const shape = collapsed
      ? 'w-10 h-10 mx-auto justify-center'
      : 'gap-3 px-3 py-2.5 w-full';
    return `flex items-center rounded-lg text-sm font-medium transition-colors ${shape} ${
      isActive
        ? 'bg-[var(--ao-brand)] text-white'
        : 'text-[var(--ao-text-muted)] hover:bg-[var(--ao-surface-hover)] hover:text-[var(--ao-text)]'
    }`;
  }

  return (
    <div className="flex h-screen">
      {/* Sidebar */}
      <aside
        className={`flex flex-col border-r border-[var(--ao-border)] bg-[var(--ao-surface)] transition-[width] duration-150 ${
          collapsed ? 'w-16' : 'w-60'
        }`}
      >
        {/* Logo */}
        <div className={`flex items-center gap-2 py-5 ${collapsed ? 'justify-center px-0' : 'px-5'}`}>
          <img src="/logo.png" alt="AgentOven" className="w-8 h-8 rounded-full object-cover shrink-0" />
          {!collapsed && (
            <span className="text-lg font-bold text-[var(--ao-brand-light)] truncate">
              AgentOven
            </span>
          )}
        </div>

        {/* Search */}
        {!collapsed && (
          <div className="px-3 pb-2">
            <input
              type="text"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder="Search navigation…"
              aria-label="Search navigation"
              className="w-full px-3 py-1.5 rounded-lg text-sm bg-[var(--ao-bg)] border border-[var(--ao-border)] text-[var(--ao-text)] placeholder:text-[var(--ao-text-muted)] outline-none focus:border-[var(--ao-brand)]"
            />
          </div>
        )}

        {/* Nav */}
        <nav className="flex-1 px-3 space-y-1 overflow-y-auto">
          {showOverview && (
            <NavLink key={overviewItem.to} to={overviewItem.to} end className={navLinkClass} title={collapsed ? overviewItem.label : undefined}>
              <overviewItem.icon size={18} className="shrink-0" />
              {!collapsed && overviewItem.label}
            </NavLink>
          )}

          {filteredGroups.map((group, groupIndex) => {
            const isExpanded = isSearching || expandedGroups.includes(group.id);
            return (
              <div key={group.id} className={collapsed ? '' : 'pt-2'}>
                {collapsed && groupIndex > 0 && (
                  <div aria-hidden className="h-px bg-[var(--ao-border)] mx-3 my-2" />
                )}
                {!collapsed && (
                  <button
                    type="button"
                    onClick={() => toggleGroup(group.id)}
                    aria-expanded={isExpanded}
                    aria-controls={`nav-group-${group.id}`}
                    className="w-full flex items-center gap-1.5 px-3 py-1 text-xs font-semibold uppercase tracking-wide text-[var(--ao-text-muted)] hover:text-[var(--ao-text)] transition-colors"
                  >
                    {isExpanded ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
                    {group.label}
                  </button>
                )}
                {(collapsed || isExpanded) && (
                  <div id={`nav-group-${group.id}`} className="space-y-1 mt-1">
                    {group.items.map(({ to, label, icon: Icon }) => (
                      <NavLink key={to} to={to} className={navLinkClass} title={collapsed ? label : undefined}>
                        <Icon size={18} className="shrink-0" />
                        {!collapsed && label}
                      </NavLink>
                    ))}
                  </div>
                )}
              </div>
            );
          })}

          {isSearching && filteredGroups.length === 0 && !showOverview && (
            <p className="px-3 py-2 text-xs text-[var(--ao-text-muted)]">No matches for "{search}"</p>
          )}
        </nav>

        {/* Collapse toggle */}
        <button
          type="button"
          onClick={() => setCollapsed((v) => !v)}
          aria-label={collapsed ? 'Expand sidebar' : 'Collapse sidebar'}
          className={`flex items-center gap-2 py-3 text-[var(--ao-text-muted)] hover:text-[var(--ao-text)] border-t border-[var(--ao-border)] transition-colors ${
            collapsed ? 'justify-center px-0' : 'justify-end px-5'
          }`}
        >
          {collapsed ? <ChevronsRight size={16} /> : <ChevronsLeft size={16} />}
        </button>

        {/* Footer */}
        {!collapsed && (
          <div className="px-5 py-4 text-xs text-[var(--ao-text-muted)] border-t border-[var(--ao-border)]">
            AgentOven OSS · Community
          </div>
        )}
      </aside>

      {/* Main content */}
      <main className="flex-1 overflow-auto">
        <Outlet />
      </main>
    </div>
  );
}
