import { useTranslation } from 'react-i18next'

/**
 * WhyDisclosure — "Neden bu takım?" collapsible accordion (D-13, TRUST-03).
 * Collapsed by default. Lists top_series titles from the Phase 6 DTO as a
 * bulleted list. No additional evidence in v1.1 (edge weight, member
 * overlap, etc.) per CONTEXT decision.
 */
export default function WhyDisclosure({ topSeries }) {
  const { t } = useTranslation()
  if (!topSeries || topSeries.length === 0) return null
  return (
    <details className="mt-2 text-sm">
      <summary className="cursor-pointer text-gray-600">
        {t('onboarding.teams.whyDisclosureLabel')}
      </summary>
      <ul className="mt-1 list-disc pl-5">
        {topSeries.map((title, i) => (
          <li key={i}>{title}</li>
        ))}
      </ul>
    </details>
  )
}
