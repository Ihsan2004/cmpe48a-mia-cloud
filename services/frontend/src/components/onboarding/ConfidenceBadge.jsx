import { useTranslation } from 'react-i18next'

/**
 * ConfidenceBadge — quiet color pill (D-12).
 * Format: `[● Yüksek]` (green) / `[● Orta]` (amber) / `[● Düşük]` (grey).
 * Maps Phase 6's `confidence_bucket` ('high'|'mid'|'low') to TR via i18n.
 *
 * RESEARCH "Don't Hand-Roll": this component NEVER re-tiers from a numeric
 * confidence_score — the bucket is computed server-side by Phase 6.
 * That keeps the threshold semantics in one place (params.go) and avoids
 * client/server drift.
 */
export default function ConfidenceBadge({ bucket }) {
  const { t } = useTranslation()
  const safeBucket =
    bucket === 'high' || bucket === 'mid' || bucket === 'low' ? bucket : 'low'
  const colorClass = {
    high: 'bg-green-500',
    mid: 'bg-amber-500',
    low: 'bg-gray-400',
  }[safeBucket]
  return (
    <span className="inline-flex items-center gap-1.5 rounded-full bg-gray-100 px-2 py-0.5 text-xs">
      <span className={`h-2 w-2 rounded-full ${colorClass}`} aria-hidden="true" />
      <span>{t(`onboarding.teams.confidence.${safeBucket}`)}</span>
    </span>
  )
}
