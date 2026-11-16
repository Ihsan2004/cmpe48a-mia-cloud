import { useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Search, X, Link2, Loader } from 'lucide-react'
import { listUserChains } from '../services/api'

const dateFmt = new Intl.DateTimeFormat('tr-TR', {
  day: 'numeric',
  month: 'short',
  year: 'numeric',
})

/**
 * ChainSelectorModal — picker for "move-to-chain". Loads the user's
 * chains on open, filters by the search box, and calls onSelect with
 * the chosen chain id. The chain that currently owns the meeting
 * (currentChainId) is filtered out so the user never picks a no-op
 * destination.
 *
 * Loading + error + empty + no-match are all explicit states so the
 * modal never silently shows nothing without a reason.
 */
export default function ChainSelectorModal({
  open,
  onClose,
  onSelect,
  currentChainId = null,
}) {
  const { t } = useTranslation()
  const [chains, setChains] = useState([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(null)
  const [query, setQuery] = useState('')
  const searchRef = useRef(null)

  // Reset transient state every time the modal opens; this keeps
  // stale error / search text from leaking across invocations.
  useEffect(() => {
    if (!open) return
    let cancelled = false
    setQuery('')
    setError(null)
    setLoading(true)
    listUserChains(100)
      .then((data) => {
        if (cancelled) return
        setChains(Array.isArray(data) ? data : [])
      })
      .catch((err) => {
        if (cancelled) return
        setError(err?.message || t('chain.selector.loadError'))
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [open, t])

  // Autofocus the search box once the modal mounts so keyboard users
  // can type immediately. setTimeout punts past the same-tick render.
  useEffect(() => {
    if (!open) return
    const id = window.setTimeout(() => {
      searchRef.current?.focus()
    }, 0)
    return () => window.clearTimeout(id)
  }, [open])

  // Esc closes; bound only while open to avoid leaking listeners.
  useEffect(() => {
    if (!open) return
    const onKey = (e) => {
      if (e.key === 'Escape') onClose?.()
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [open, onClose])

  const visibleChains = useMemo(() => {
    const q = query.trim().toLocaleLowerCase('tr')
    return chains
      .filter((c) => c.id !== currentChainId)
      .filter((c) => {
        if (!q) return true
        const name = (c.display_name || '').toLocaleLowerCase('tr')
        return name.includes(q)
      })
  }, [chains, query, currentChainId])

  if (!open) return null

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 px-4"
      role="dialog"
      aria-modal="true"
      aria-label={t('chain.selector.title')}
      onMouseDown={(e) => {
        // Click on backdrop closes; click on the panel doesn't.
        if (e.target === e.currentTarget) onClose?.()
      }}
    >
      <div className="w-full max-w-md bg-[var(--canvas-bone)] border border-[var(--canvas-muted)] shadow-lg rounded-md flex flex-col max-h-[80vh]">
        {/* Header */}
        <div className="flex items-start justify-between gap-3 p-4 border-b border-[var(--canvas-muted)]">
          <div>
            <h2 className="text-base font-serif text-text-primary">
              {t('chain.selector.title')}
            </h2>
            <p className="text-xs text-text-tertiary mt-0.5">
              {t('chain.selector.subtitle')}
            </p>
          </div>
          <button
            type="button"
            onClick={onClose}
            className="p-1 text-text-tertiary hover:text-text-primary transition-colors"
            aria-label={t('chain.selector.cancel')}
          >
            <X className="w-4 h-4" />
          </button>
        </div>

        {/* Search */}
        <div className="px-4 pt-3 pb-2">
          <div className="relative">
            <Search className="absolute left-2 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-text-tertiary pointer-events-none" />
            <input
              ref={searchRef}
              type="text"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder={t('chain.selector.searchPlaceholder')}
              className="w-full pl-7 pr-2 py-1.5 text-sm bg-surface border border-[var(--canvas-muted)] rounded focus:outline-none focus:border-primary/50"
            />
          </div>
        </div>

        {/* List */}
        <div className="flex-1 overflow-y-auto px-2 pb-2">
          {loading && (
            <div className="flex items-center gap-2 px-3 py-6 text-sm text-text-tertiary">
              <Loader className="w-3.5 h-3.5 animate-spin" />
              {t('chain.selector.loading')}
            </div>
          )}

          {!loading && error && (
            <div className="px-3 py-4 text-sm text-danger">{error}</div>
          )}

          {!loading && !error && visibleChains.length === 0 && (
            <div className="px-3 py-6 text-sm text-text-tertiary text-center">
              {chains.length === 0
                ? t('chain.selector.empty')
                : t('chain.selector.noMatch')}
            </div>
          )}

          {!loading && !error && visibleChains.length > 0 && (
            <ul role="listbox">
              {visibleChains.map((c) => {
                const label = c.display_name || t('chain.selector.untitled')
                const lastDate = c.last_meeting_at
                  ? dateFmt.format(new Date(c.last_meeting_at))
                  : null
                return (
                  <li key={c.id}>
                    <button
                      type="button"
                      role="option"
                      aria-selected="false"
                      onClick={() => onSelect?.(c.id)}
                      className="w-full text-left px-3 py-2 rounded hover:bg-surface flex items-start gap-2"
                    >
                      <Link2 className="w-3.5 h-3.5 text-text-tertiary mt-0.5 shrink-0" />
                      <div className="flex-1 min-w-0">
                        <div className="text-sm text-text-primary truncate">
                          {label}
                        </div>
                        <div className="text-xs text-text-tertiary mt-0.5 flex items-center gap-2">
                          {lastDate && (
                            <span>
                              {t('chain.selector.lastMeeting')}: {lastDate}
                            </span>
                          )}
                          <span>
                            {t('chain.selector.meetingCount', { count: c.meeting_count || 0 })}
                          </span>
                        </div>
                      </div>
                    </button>
                  </li>
                )
              })}
            </ul>
          )}
        </div>

        {/* Footer */}
        <div className="px-4 py-3 border-t border-[var(--canvas-muted)] flex justify-end">
          <button
            type="button"
            onClick={onClose}
            className="px-3 py-1.5 text-sm text-text-secondary hover:text-text-primary transition-colors"
          >
            {t('chain.selector.cancel')}
          </button>
        </div>
      </div>
    </div>
  )
}
