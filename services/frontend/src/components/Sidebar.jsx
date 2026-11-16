import { Link, useLocation } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Home, Calendar, GitBranch, Settings, LogOut, PanelLeftClose, PanelLeftOpen, Bell, Users, Sparkles, Gauge } from 'lucide-react'

export default function Sidebar({ user, onLogout, collapsed = false, onToggle }) {
  const location = useLocation()
  const { t } = useTranslation()

  const navItems = [
    { path: '/dashboard', labelKey: 'sidebar.home', icon: Home },
    { path: '/meetings', labelKey: 'sidebar.pastMeetings', icon: Calendar },
    { path: '/flows', labelKey: 'sidebar.flows', icon: GitBranch },
    // Phase 7 (D-22): 'Takımlar' visible only post-onboarding-finalize.
    // RESEARCH Pitfall 2: explicit truthy + not-pending check defensively
    // handles the brief window where user is loaded but the field is
    // undefined.
    ...(user?.teams_onboarding_status && user.teams_onboarding_status !== 'pending'
      ? [{ path: '/teams', labelKey: 'sidebar.teams', icon: Users }]
      : []),
    { path: '/chain-suggestions', labelKey: 'sidebar.chainSuggestions', icon: Sparkles },
    { path: '/settings/notifications', labelKey: 'sidebar.notifications', icon: Bell },
    { path: '/settings/usage', label: 'Kullanım', icon: Gauge },
  ]

  const isActive = (path) => location.pathname === path

  return (
    <div
      className={`${collapsed ? 'w-20' : 'w-72'} flex flex-col h-screen transition-all duration-300 ease-out bg-[var(--canvas-bone)] border-r border-[var(--canvas-muted)]`}
    >
      {/* Wordmark */}
      {!collapsed && (
        <div className="px-6 pt-8 pb-6">
          <Link to="/dashboard" className="font-serif font-medium tracking-tight text-2xl leading-none text-[var(--ink-charcoal)]">
            syncrun
          </Link>
        </div>
      )}

      {/* Toggle Button */}
      <div className={`flex ${collapsed ? 'justify-center pt-6' : 'justify-end'} px-3 pb-2`}>
        <button
          onClick={onToggle}
          className="p-2 rounded-full text-[var(--ink-secondary)] hover:text-[var(--ink-charcoal)] transition-colors duration-150 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent-ochre-ink)] focus-visible:ring-offset-2 focus-visible:ring-offset-[var(--canvas-bone)]"
          title={collapsed ? 'Menüyü aç' : 'Menüyü kapat'}
        >
          {collapsed ? <PanelLeftOpen className="w-5 h-5" /> : <PanelLeftClose className="w-5 h-5" />}
        </button>
      </div>

      {/* Navigation */}
      <nav className="flex-1 px-3 pt-2 pb-4 space-y-1">
        {navItems.map((item) => {
          const Icon = item.icon
          const active = isActive(item.path)
          // Sidebar items use either `labelKey` (i18n) or a static
          // `label` string (issue #39 — Kullanım: Turkish-only UI for
          // now, no en.json key yet).
          const label = item.labelKey ? t(item.labelKey) : item.label

          return (
            <Link
              key={item.path}
              to={item.path}
              className={`
                group flex items-center gap-3 px-4 py-2.5 transition-colors duration-150
                ${active
                  ? 'text-[var(--ink-charcoal)]'
                  : 'text-[var(--ink-secondary)] hover:text-[var(--ink-charcoal)]'
                }
              `}
              title={label}
            >
              <Icon className={`w-[18px] h-[18px] ${active ? 'text-[var(--accent-ochre-ink)]' : ''}`} />

              {!collapsed && (
                <span
                  className={`text-meta ${
                    active
                      ? 'font-medium underline underline-offset-[8px] decoration-[var(--accent-ochre-ink)]'
                      : 'font-normal'
                  }`}
                >
                  {label}
                </span>
              )}
            </Link>
          )
        })}
      </nav>

      {/* User Section */}
      <div className="p-6 mx-3 mb-4 border-t border-[var(--canvas-muted)]">
        <div className="flex items-center gap-3 mb-4">
          <div className="w-10 h-10 rounded-full flex items-center justify-center font-serif text-lg text-[var(--canvas-bone)] bg-[var(--ink-charcoal)]">
            {user?.full_name?.[0]?.toUpperCase() || user?.email?.[0]?.toUpperCase() || 'U'}
          </div>

          {!collapsed && (
            <div className="flex-1 min-w-0">
              <div className="text-meta font-medium text-[var(--ink-charcoal)] truncate">
                {user?.full_name || t('common.user')}
              </div>
              <div className="text-micro text-[var(--ink-secondary)] truncate">
                {user?.email}
              </div>
            </div>
          )}
        </div>

        <div className={`flex ${collapsed ? 'flex-col' : ''} gap-2`}>
          <Link
            to="/profile"
            className="flex-1 flex items-center justify-center gap-2 px-3 py-2 rounded-full text-[var(--ink-secondary)] hover:text-[var(--ink-charcoal)] transition-colors duration-150 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent-ochre-ink)] focus-visible:ring-offset-2 focus-visible:ring-offset-[var(--canvas-bone)]"
            title={t('sidebar.settings')}
          >
            <Settings className="w-4 h-4" />
            {!collapsed && <span className="text-meta">{t('sidebar.settings')}</span>}
          </Link>

          <button
            onClick={onLogout}
            className="flex-1 flex items-center justify-center gap-2 px-3 py-2 rounded-full text-[var(--ink-secondary)] hover:text-[var(--ink-charcoal)] transition-colors duration-150 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent-ochre-ink)] focus-visible:ring-offset-2 focus-visible:ring-offset-[var(--canvas-bone)]"
            title={t('sidebar.logout')}
          >
            <LogOut className="w-4 h-4" />
            {!collapsed && <span className="text-meta">{t('sidebar.logout')}</span>}
          </button>
        </div>
      </div>
    </div>
  )
}
