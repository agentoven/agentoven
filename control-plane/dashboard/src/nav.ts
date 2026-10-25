import {
  LayoutDashboard, Bot, BookOpen, Cpu, Wrench, Activity, FileText, Search, Plug, Sparkles, GitBranch, Hammer,
  type LucideIcon,
} from 'lucide-react';

export interface NavItem {
  to: string;
  label: string;
  icon: LucideIcon;
  /** Other paths that belong to this item, so it stays selected on its tabs and detail pages. */
  also?: string[];
}

export interface NavGroup {
  label?: string;
  items: NavItem[];
}

export interface NavSection {
  id: string;
  label: string;
  icon: LucideIcon;
  groups: NavGroup[];
}

/**
 * The sidebar: a rail of sections, and a panel with the items of the selected one. Every route keeps its
 * address; only where it is reached from changed.
 */
export const NAV_SECTIONS: NavSection[] = [
  {
    id: 'home', label: 'Home', icon: LayoutDashboard,
    groups: [{ items: [{ to: '/', label: 'Overview', icon: LayoutDashboard }] }],
  },
  {
    id: 'build', label: 'Build', icon: Hammer,
    groups: [
      {
        items: [
          { to: '/agents', label: 'Agents', icon: Bot },
          { to: '/recipes', label: 'Recipes', icon: BookOpen },
          { to: '/dishshelf', label: 'DishShelf', icon: GitBranch },
        ],
      },
      {
        label: 'Ingredients',
        items: [
          { to: '/providers', label: 'Providers', icon: Cpu, also: ['/catalog'] },
          { to: '/skills', label: 'Skills', icon: Sparkles },
          { to: '/prompts', label: 'Prompts', icon: FileText },
          { to: '/tools', label: 'Tools', icon: Wrench },
          { to: '/rag', label: 'Knowledge', icon: Search, also: ['/embeddings', '/vectorstores'] },
          { to: '/connectors', label: 'Connectors', icon: Plug },
        ],
      },
    ],
  },
  {
    id: 'observe', label: 'Observe', icon: Activity,
    groups: [{ items: [{ to: '/traces', label: 'Traces', icon: Activity }] }],
  },
];

export function itemMatches(item: NavItem, pathname: string): boolean {
  if (item.to === '/') return pathname === '/';
  return [item.to, ...(item.also ?? [])].some((p) => pathname === p || pathname.startsWith(p + '/'));
}

export function sectionMatches(section: NavSection, pathname: string): boolean {
  return section.groups.some((g) => g.items.some((i) => itemMatches(i, pathname)));
}

/** Pages that share a hub show these tabs under their title. */
export const PROVIDER_TABS = [{ to: '/providers', label: 'Providers' }, { to: '/catalog', label: 'Models' }];
export const KNOWLEDGE_TABS = [
  { to: '/rag', label: 'Pipelines' },
  { to: '/embeddings', label: 'Embedding models' },
  { to: '/vectorstores', label: 'Vector stores' },
];
