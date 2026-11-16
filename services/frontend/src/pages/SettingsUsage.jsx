import { useEffect } from 'react'
import { Gauge, AlertTriangle, Infinity as InfinityIcon, Sparkles } from 'lucide-react'
import { useQuota } from '../context/QuotaContext'
import LoadingState from '../components/LoadingState'

// SettingsUsage = /settings/usage. Per Pivot UX kararı, kullanıcı dolar
// tutarı görmez — yalnızca yüzde + reset tarihi + tier rozeti.
//
// Three visual states:
//   - internal tier        → "Limitsiz kullanım" rozet + reset bilgisi
//   - finite tier (< 80%)  → progress bar, ölçülü ton
//   - finite tier (>= 80%) → progress bar + uyarı (banner App seviyesinde de var)

const TIER_COPY = {
  free: {
    badge: 'Free plan',
    description: 'Aylık ücretsiz kullanım hakkın. Daha fazlasına ihtiyacın olursa iletişime geç.',
  },
  poc: {
    badge: 'POC programı',
    description: 'Erken erişim ortağı olarak genişletilmiş aylık limit.',
  },
  internal: {
    badge: 'Limitsiz',
    description: 'Ekip içi hesap — kotaya tabi değil.',
  },
}

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

export default function SettingsUsage() {
  const { status, loading, error, refetch, isUnlimited } = useQuota()

  useEffect(() => {
    refetch()
  }, [refetch])

  if (loading && !status) {
    return <LoadingState />
  }

  const tierKey = status?.tier || 'free'
  const tierMeta = TIER_COPY[tierKey] || TIER_COPY.free
  const usedPct = Math.max(0, Math.min(100, Number(status?.used_pct ?? 0)))
  const resetDate = formatResetDate(status?.cycle_resets_at)
  const warn = !isUnlimited && usedPct >= 80
  const exceeded = !isUnlimited && usedPct >= 100

  return (
    <div className="max-w-3xl mx-auto px-6 py-10">
      <header className="mb-10">
        <div className="flex items-center gap-3 mb-2">
          <Gauge className="w-6 h-6 text-[var(--accent-ochre-ink)]" />
          <h1 className="text-2xl font-semibold text-text-primary">Aylık kullanım</h1>
        </div>
        <p className="text-text-secondary text-sm">
          Bu döngüde kotanın ne kadarını kullandığını burada görebilirsin.
        </p>
      </header>

      {error && (
        <div className="mb-6 flex items-start gap-2 p-3 rounded-lg bg-amber-50 border border-amber-200 text-amber-900">
          <AlertTriangle className="w-4 h-4 mt-0.5 flex-shrink-0" />
          <p className="text-meta">Kota durumu alınamadı. Birazdan tekrar denenecek.</p>
        </div>
      )}

      {/* Tier card */}
      <section className="bg-[var(--canvas-muted)] border border-[var(--line-hair)] rounded-xl p-5 mb-6">
        <div className="flex items-start gap-3">
          {isUnlimited ? (
            <InfinityIcon className="w-5 h-5 mt-0.5 text-text-primary" />
          ) : (
            <Sparkles className="w-5 h-5 mt-0.5 text-text-primary" />
          )}
          <div className="flex-1">
            <div className="flex items-center gap-2">
              <h2 className="font-medium text-text-primary">{tierMeta.badge}</h2>
              <span
                className="text-micro px-2 py-0.5 rounded-full bg-[var(--canvas-bone)] border border-[var(--line-hair)] text-text-secondary"
                data-testid="tier-badge"
              >
                {tierKey}
              </span>
            </div>
            <p className="text-meta text-text-secondary mt-1">{tierMeta.description}</p>
          </div>
        </div>
      </section>

      {/* Progress bar — skipped on internal tier (no cap, no bar) */}
      {!isUnlimited && (
        <section className="bg-[var(--canvas-muted)] border border-[var(--line-hair)] rounded-xl p-5 mb-6">
          <div className="flex items-baseline justify-between mb-3">
            <h2 className="font-medium text-text-primary">Bu döngünün kullanımı</h2>
            <span
              className={`text-xl font-semibold ${
                exceeded ? 'text-red-700' : warn ? 'text-amber-700' : 'text-text-primary'
              }`}
              data-testid="usage-pct"
            >
              %{usedPct}
            </span>
          </div>

          <div className="h-2 w-full rounded-full bg-[var(--canvas-bone)] overflow-hidden">
            <div
              role="progressbar"
              aria-valuenow={usedPct}
              aria-valuemin={0}
              aria-valuemax={100}
              data-testid="usage-bar"
              className={`h-full transition-all duration-300 ${
                exceeded
                  ? 'bg-red-600'
                  : warn
                    ? 'bg-amber-500'
                    : 'bg-[var(--accent-ochre-ink)]'
              }`}
              style={{ width: `${usedPct}%` }}
            />
          </div>

          {resetDate ? (
            <p className="text-meta text-text-secondary mt-3">
              {exceeded
                ? `Kota doldu. ${resetDate} tarihinde sıfırlanacak.`
                : `${resetDate} tarihinde sıfırlanacak.`}
            </p>
          ) : (
            <p className="text-meta text-text-secondary mt-3">
              Reset tarihi şu an alınamıyor.
            </p>
          )}
        </section>
      )}

      {/* Reset info on internal tier */}
      {isUnlimited && resetDate && (
        <p className="text-meta text-text-secondary">
          Sayaç {resetDate} tarihinde sıfırlanır. Senin için pratik bir etkisi yok.
        </p>
      )}
    </div>
  )
}
