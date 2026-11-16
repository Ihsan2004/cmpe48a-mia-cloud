import { useTranslation } from 'react-i18next'

/**
 * SaveConfirmStrip — sticky footer for the edit page (D-15).
 *
 * Two visual states:
 *   - isConfirming=false: "Kaydet (X takım, Y üye)" + "Şimdilik atla"
 *   - isConfirming=true:  inline strip "Kaydetmeye hazır mısın? — X takım, Y üye [Geri] [Onayla]"
 *
 * Pressing Kaydet calls onSave (parent flips isConfirming=true). Pressing
 * Onayla calls onConfirm (parent fires saveTeams API). Pressing Geri calls
 * onBack (parent flips isConfirming=false). isSaving disables the Onayla
 * button and shows a spinner state.
 */
export default function SaveConfirmStrip({
  teamCount,
  memberCount,
  isConfirming,
  isSaving,
  onSave,
  onConfirm,
  onBack,
  onSkip,
  saveDisabled,
}) {
  const { t } = useTranslation()

  if (isConfirming) {
    return (
      <div className="sticky bottom-0 flex items-center gap-3 border-t border-gray-200 bg-white p-4">
        <span className="flex-1 text-sm">
          {t('onboarding.teams.saveConfirmStrip', {
            teams: teamCount,
            members: memberCount,
          })}
        </span>
        <button
          type="button"
          onClick={onBack}
          disabled={isSaving}
          className="rounded border px-3 py-1 text-sm"
        >
          {t('onboarding.teams.saveConfirmBack')}
        </button>
        <button
          type="button"
          onClick={onConfirm}
          disabled={isSaving}
          className="rounded bg-blue-600 px-3 py-1 text-sm text-white disabled:opacity-50"
        >
          {isSaving ? '…' : t('onboarding.teams.saveConfirmCta')}
        </button>
      </div>
    )
  }

  return (
    <div className="sticky bottom-0 flex items-center justify-between border-t border-gray-200 bg-white p-4">
      <button
        type="button"
        onClick={onSkip}
        className="text-sm text-gray-500 hover:text-gray-700 underline"
      >
        {t('onboarding.teams.skip')}
      </button>
      <button
        type="button"
        onClick={onSave}
        disabled={saveDisabled}
        className="rounded bg-blue-600 px-4 py-2 text-sm text-white disabled:opacity-50"
      >
        {t('onboarding.teams.saveButton', { teams: teamCount, members: memberCount })}
      </button>
    </div>
  )
}
