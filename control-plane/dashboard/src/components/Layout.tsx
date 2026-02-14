import { NavLink, Outlet } from 'react-router-dom';
import {
  LayoutDashboard,
  Bot,
  BookOpen,
  Cpu,
  Wrench,
  Activity,
} from 'lucide-react';

const nav = [
  { to: '/', label: 'Overview', icon: LayoutDashboard },
  { to: '/agents', label: 'Agents', icon: Bot },
  { to: '/recipes', label: 'Recipes', icon: BookOpen },
  { to: '/providers', label: 'Providers', icon: Cpu },
  { to: '/tools', label: 'Tools', icon: Wrench },
  { to: '/traces', label: 'Traces', icon: Activity },
];

export function Layout() {
  return (
    <div className="flex h-screen">
      {/* Sidebar */}
      <aside className="w-60 flex flex-col border-r border-[var(--ao-border)] bg-[var(--ao-surface)]">
        {/* Logo */}
        <div className="flex items-center gap-2 px-5 py-5">
          <span className="text-2xl">🏺</span>
          <span className="text-lg font-bold text-[var(--ao-brand-light)]">
            AgentOven
          </span>
        </div>

        {/* Nav */}
        <nav className="flex-1 px-3 space-y-1">
          {nav.map(({ to, label, icon: Icon }) => (
            <NavLink
              key={to}
              to={to}
              end={to === '/'}
              className={({ isActive }) =>
                `flex items-center gap-3 px-3 py-2.5 rounded-lg text-sm font-medium transition-colors ${
                  isActive
                    ? 'bg-[var(--ao-brand)] text-white'
                    : 'text-[var(--ao-text-muted)] hover:bg-[var(--ao-surface-hover)] hover:text-[var(--ao-text)]'
                }`
              }
            >
              <Icon size={18} />
              {label}
            </NavLink>
          ))}
        </nav>

        {/* Footer */}
        <div className="px-5 py-4 text-xs text-[var(--ao-text-muted)] border-t border-[var(--ao-border)]">
          AgentOven OSS · Community
        </div>
      </aside>

      {/* Main content */}
      <main className="flex-1 overflow-auto">
        <Outlet />
      </main>
    </div>
  );
}
