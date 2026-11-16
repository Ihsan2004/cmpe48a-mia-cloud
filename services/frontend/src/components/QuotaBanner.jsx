import { Link } from 'react-router-dom'
import { useEffect, useState } from 'react'
import { AlertTriangle, X } from 'lucide-react'
import { useQuota } from '../context/QuotaContext'

// QuotaBanner is mounted once at the App shell. Two visual surfaces
// driven from the same QuotaContext:
//
//   - Inline banner: shown when usage >= 80% (and not yet exceeded).
//     Pasif uyarı; kullanıcı çalışmaya devam edebilir, sadece haberdar
//     olur.
//   - Modal: shown when an API call returned 429 (`quota:exceeded`
//     dispatched from authFetch) OR usage is at 100%. Blocks new
//     actions but doesn't kick the user out — they can still browse.
//
// Internal tier never sees either surface (unlimited cap).

function formatResetDate(iso) {
  if (!iso) return null
  try {
    const d = new Date(iso)
    if (Number.isNaN(d.getTime())) return null
    return d.toLocaleDateString('tr-TR', { day: 'numeric', month: 'long', year: 'numeric' })
  } catch {
    return null
  }
}

export default function QuotaBanner() {
  const { status, exceededAt, isUnlimited, dismissModal } = useQuota()
  // Two independent dismiss-stamps:
  //   - dismissedCycleKey suppresses the "polling alone showed 100%"
  //     case so the modal doesn't keep popping back from background
  //     refetches during the same cycle.
  //   - dismissedExceededAt suppresses the SPECIFIC 429 push event the
  //     user clicked away. A LATER 429 from a different endpoint
  //     updates exceededAt → the inequality fires → modal reopens.
  // Issue #39 review fix: previously dismiss permanently silenced all
  // future 429 pushes for the cycle.
  const [dismissedCycleKey, setDismissedCycleKey] = useState(null)
  const [dismissedExceededAt, setDismissedExceededAt] = useState(null)

  const usedPct = Math.max(0, Math.min(100, Number(status?.used_pct ?? 0)))
  const resetDate = formatResetDate(status?.cycle_resets_at)
  const cycleKey = status?.cycle_resets_at || 'unknown-cycle'

  useEffect(() => {
    if (usedPct < 100) {
      setDismissedCycleKey(null)
    }
  }, [usedPct, cycleKey])

  if (isUnlimited || !status) return null

  // Modal triggers, evaluated independently:
  //   - push: a new exceededAt that the user hasn't dismissed yet.
  //     Each 429 from any endpoint stamps a fresh exceededAt timestamp,
  //     so this fires once per event.
  //   - polling: usedPct >= 100% reported by the periodic refetch.
  //     Suppressed for the cycle once dismissed.
  const pushTrigger = exceededAt !== null && exceededAt !== dismissedExceededAt
  const pollingTrigger = usedPct >= 100 && dismissedCycleKey !== cycleKey
  const modalOpen = pushTrigger || pollingTrigger
  // Banner shows the 80–99% warning band only — at 100% the modal owns
  // the surface so the banner doesn't double up.
  const bannerOpen = !modalOpen && usedPct >= 80

  const handleDismiss = () => {
    setDismissedCycleKey(cycleKey)
    setDismissedExceededAt(exceededAt)
    dismissModal()
  }

  return (
    <>
      {bannerOpen && (
        <div
          role="status"
          data-testid="quota-banner"
          className="bg-amber-50 border-b border-amber-200 text-amber-900 px-4 py-2 text-meta flex items-center gap-2"
        >
          <AlertTriangle className="w-4 h-4 flex-shrink-0" />
          <span className="flex-1">
            Bu döngünün kotasının %{usedPct}'ini kullandın.
            {resetDate ? ` ${resetDate} tarihinde sıfırlanacak.` : ''}
          </span>
          <Link
            to="/settings/usage"
            className="underline hover:opacity-80 font-medium"
          >
            Detay
          </Link>
        </div>
      )}

      {modalOpen && (
        <div
          role="dialog"
          aria-modal="true"
          aria-labelledby="quota-modal-title"
          data-testid="quota-modal"
          className="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/40"
        >
          <div className="bg-[var(--canvas-bone)] rounded-xl shadow-xl max-w-md w-full p-6 relative">
            <button
              type="button"
              onClick={handleDismiss}
              aria-label="Kapat"
              className="absolute top-3 right-3 p-1 rounded-full text-text-secondary hover:text-text-primary"
              data-testid="quota-modal-close"
            >
              <X className="w-4 h-4" />
            </button>

            <div className="flex items-start gap-3 mb-4">
              <div className="w-10 h-10 rounded-full bg-red-100 flex items-center justify-center flex-shrink-0">
                <AlertTriangle className="w-5 h-5 text-red-600" />
              </div>
              <div>
                <h2
                  id="quota-modal-title"
                  className="text-lg font-semibold text-text-primary"
                >
                  Bu döngünün kotası doldu
                </h2>
                <p className="text-meta text-text-secondary mt-1">
                  {resetDate
                    ? `Kota ${resetDate} tarihinde sıfırlanacak. O zamana kadar yeni bot oturumu ya da manuel çıkarım başlatılamıyor.`
                    : 'Kota bu döngünün sonunda sıfırlanacak. Yeni bot oturumu ya da manuel çıkarım şu an başlatılamıyor.'}
                </p>
              </div>
            </div>

            <div className="flex items-center justify-end gap-2 mt-6">
              <Link
                to="/settings/usage"
                onClick={handleDismiss}
                className="text-meta px-4 py-2 rounded-full bg-[var(--accent-ochre-ink)] text-[var(--canvas-bone)] hover:opacity-90"
              >
                Kullanımı gör
              </Link>
              <button
                type="button"
                onClick={handleDismiss}
                className="text-meta px-4 py-2 rounded-full text-text-secondary hover:text-text-primary"
              >
                Anladım
              </button>
            </div>
          </div>
        </div>
      )}
    </>
  )
}
