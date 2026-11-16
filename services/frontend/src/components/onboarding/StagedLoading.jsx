import { useTranslation } from 'react-i18next'

/**
 * StagedLoading — two-stage Turkish loading screen (D-30, ONBOARD-04).
 *
 * Props:
 *   - stage: 1 (calendar fetch) or 2 (detection running)
 *   - onSkip?: optional "Şimdilik atla" handler — visible on this screen too
 *              per D-18 ("visible on every onboarding screen")
 *
 * The time-split logic (advance stage 1 → 2 after min 600ms) lives in the
 * parent OnboardingTeams.jsx — this component is a pure render.
 */
export default function StagedLoading({ stage, onSkip }) {
  const { t } = useTranslation()
  const messageKey =
    stage === 2 ? 'onboarding.teams.loadingDetect' : 'onboarding.teams.loadingFetch'
  return (
    <div className="flex h-screen flex-col items-center justify-center gap-6">
      <div
        className="h-8 w-8 animate-spin rounded-full border-2 border-blue-500 border-t-transparent"
        aria-hidden="true"
      />
      <p className="text-lg" aria-live="polite">
        {t(messageKey)}
      </p>
      {onSkip && (
        <button
          type="button"
          onClick={onSkip}
          className="text-sm text-gray-500 hover:text-gray-700 underline"
        >
          {t('onboarding.teams.skip')}
        </button>
      )}
    </div>
  )
}
