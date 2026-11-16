import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  Link2,
  Link2Off,
  Sparkles,
  RefreshCw,
  User,
  Calendar,
  Bot,
  MoreHorizontal,
  Move,
  X,
} from 'lucide-react'

// Visual mapping for the six chain_assignment_method values plus a
// legacy fallback for meetings ingested before Phase 1 of the chain
// detection v3 rollout (where chain_assignment_method is NULL but the
// meeting still has a chain_id).
//
// Each entry pins the icon, the badge palette tokens, and the i18n
// keys for label + tooltip. Centralised here so the same visual
// language can be reused outside this badge (e.g., chain inbox row,
// brief generator UI) without copy-paste.
const METHOD_VARIANTS = {
  series_id: {
    icon: Calendar,
    classes: 'text-[var(--ink-charcoal)] bg-[var(--canvas-muted)]',
  },
  auto_score: {
    icon: Bot,
    classes: 'text-primary bg-primary/10',
  },
  llm_suggestion: {
    icon: Sparkles,
    classes: 'text-[var(--accent-ochre-ink)] bg-[var(--accent-ochre-bg)]',
  },
  decision_match: {
    icon: Sparkles,
    classes: 'text-[var(--accent-ochre-ink)] bg-[var(--accent-ochre-bg)]',
  },
  manual_user: {
    icon: User,
    classes: 'text-primary bg-primary/10',
  },
  manual_removed: {
    icon: Link2Off,
    classes: 'text-text-tertiary bg-surface',
  },
}

// Methods where "Retry auto-detect" actually does something useful.
// Limited to manual_removed because retry only clears the lock; it
// does NOT touch chain_id. For a manual_user meeting (chain_id set,
// lock active), the matcher's already-assigned bypass would still
// skip the next ingest — making retry a silent no-op. The honest
// flow for manual_user is "Remove from chain" first, then "Retry
// auto-detect" on the resulting manual_removed state.
const RETRY_VALID_METHODS = new Set(['manual_removed'])

/**
 * ChainBadge — surfaces a meeting's chain assignment + provenance and
 * lets the user correct it.
 *
 * Renders four shapes depending on input state:
 *
 *  - chainAssignmentMethod = 'manual_removed' → "Sen çıkardın" badge
 *    with retry action (chain_id is NULL but the lock is set).
 *  - chainId set + chainAssignmentMethod set → method-specific badge
 *    (e.g. "Otomatik tespit") + actions menu.
 *  - chainId set, no method (legacy backfill miss) → falls back to
 *    the chainType label.
 *  - chainId nil + no method → "Zincir yok" chip.
 *
 * Action callbacks are optional. When omitted the actions menu hides
 * itself rather than rendering a no-op trigger.
 */
export default function ChainBadge({
  chainId,
  chainType,
  chainAssignmentMethod,
  meetingId,
  onRemoveChain,
  onRetryAutoDetect,
  onMoveToChain,
}) {
  const { t } = useTranslation()
  const [menuOpen, setMenuOpen] = useState(false)
  const menuRef = useRef(null)

  // Close menu on outside click. Bound only while open to avoid the
  // listener overhead and React Strict Mode double-attach noise.
  useEffect(() => {
    if (!menuOpen) return
    const onClick = (e) => {
      if (menuRef.current && !menuRef.current.contains(e.target)) {
        setMenuOpen(false)
      }
    }
    document.addEventListener('mousedown', onClick)
    return () => document.removeEventListener('mousedown', onClick)
  }, [menuOpen])

  const hasMethod = !!chainAssignmentMethod && METHOD_VARIANTS[chainAssignmentMethod]
  const isManualRemoved = chainAssignmentMethod === 'manual_removed'
  const canRetry = RETRY_VALID_METHODS.has(chainAssignmentMethod)
  const hasAnyAction =
    !!onRemoveChain || (canRetry && !!onRetryAutoDetect) || !!onMoveToChain

  // Empty state — no chain, no method.
  if (!chainId && !hasMethod) {
    return (
      <span className="inline-flex items-center gap-1 text-xs text-text-tertiary bg-surface px-2 py-1 rounded">
        <Link2Off className="w-3 h-3" />
        {t('chain.noChain')}
      </span>
    )
  }

  // Resolve the visible label. Method takes precedence; legacy chain
  // rows without a method tag fall back to the type label.
  let icon = Link2
  let classes = 'text-primary bg-primary/10'
  let label = t('chain.inChain')
  let tooltip
  if (hasMethod) {
    const variant = METHOD_VARIANTS[chainAssignmentMethod]
    icon = variant.icon
    classes = variant.classes
    label = t(`chain.method.${chainAssignmentMethod}`)
    tooltip = t(`chain.methodTooltip.${chainAssignmentMethod}`)
  } else if (chainType) {
    // Legacy fallback for backfill misses; surfaces the raw chain_type
    // label so the user still sees something meaningful.
    label = legacyChainTypeLabel(chainType)
  }

  const Icon = icon

  return (
    <div className="inline-flex items-center gap-2" ref={menuRef}>
      <span
        className={`inline-flex items-center gap-1 text-xs px-2 py-1 rounded ${classes}`}
        title={tooltip}
      >
        <Icon className="w-3 h-3" />
        {label}
      </span>

      {hasAnyAction && (
        <div className="relative">
          <button
            type="button"
            onClick={() => setMenuOpen((v) => !v)}
            className="inline-flex items-center justify-center w-5 h-5 text-text-tertiary hover:text-text-primary transition-colors rounded"
            title={t('chain.actions.manage')}
            aria-haspopup="menu"
            aria-expanded={menuOpen}
          >
            <MoreHorizontal className="w-4 h-4" />
          </button>

          {menuOpen && (
            <div
              className="absolute right-0 z-50 mt-1 min-w-[200px] bg-[var(--canvas-bone)] border border-[var(--canvas-muted)] shadow-md py-1 text-sm font-sans"
              role="menu"
            >
              {/* Move to another chain — opens the chain picker.
                  Available whenever the consumer wires the callback;
                  works for manual_removed too (matcher won't fight
                  back because the user just made an explicit choice).
                  Listed first because it's the most constructive
                  action ("fix this" rather than "undo this"). */}
              {onMoveToChain && (
                <button
                  type="button"
                  className="flex items-center gap-2 w-full px-3 py-2 text-left text-text-primary hover:bg-surface"
                  onClick={() => {
                    setMenuOpen(false)
                    onMoveToChain(meetingId)
                  }}
                  role="menuitem"
                >
                  <Move className="w-3.5 h-3.5 text-text-tertiary" />
                  {t('chain.actions.moveToChain')}
                </button>
              )}

              {/* Retry auto-detect — only meaningful for manual_removed
                  (clears the lock; matcher then re-evaluates because
                  chain_id is already NULL). For manual_user the
                  matcher would still skip via its already-assigned
                  bypass, so we hide the action and keep the user on
                  the explicit Remove → Retry two-step. */}
              {canRetry && onRetryAutoDetect && (
                <button
                  type="button"
                  className="flex items-center gap-2 w-full px-3 py-2 text-left text-text-primary hover:bg-surface"
                  onClick={() => {
                    setMenuOpen(false)
                    onRetryAutoDetect(meetingId)
                  }}
                  role="menuitem"
                >
                  <RefreshCw className="w-3.5 h-3.5 text-text-tertiary" />
                  {t('chain.actions.retryAutoDetect')}
                </button>
              )}

              {/* Remove — hidden when the meeting is already manual_removed
                  (chain is NULL; nothing to remove). */}
              {onRemoveChain && !isManualRemoved && (
                <button
                  type="button"
                  className="flex items-center gap-2 w-full px-3 py-2 text-left text-text-primary hover:bg-surface"
                  onClick={() => {
                    setMenuOpen(false)
                    onRemoveChain(meetingId)
                  }}
                  role="menuitem"
                >
                  <X className="w-3.5 h-3.5 text-text-tertiary" />
                  {t('chain.actions.removeFromChain')}
                </button>
              )}
            </div>
          )}
        </div>
      )}
    </div>
  )
}

// legacyChainTypeLabel reproduces the pre-Phase-3 mapping so meetings
// stuck on the old chain_type-only metadata still render a friendly
// label until backfill catches them up.
function legacyChainTypeLabel(chainType) {
  const map = {
    calendar_series: 'Tekrarlayan Seri',
    participant_overlap_title: 'Katılımcı + Başlık',
    recurring_series: 'Tekrarlayan Seri',
    participant_title_match: 'Katılımcı + Başlık',
  }
  return map[chainType] || 'Zincirde'
}
