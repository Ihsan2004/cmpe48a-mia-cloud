import { useState } from 'react'
import { useTranslation } from 'react-i18next'

/**
 * MergePanel — D-07 inline merge UI.
 *
 * Props:
 *   - currentTeam: { name } — used to pre-fill the resulting-name input
 *   - candidateTeams: [{ uiId, name }] — other teams to merge INTO/FROM (excluding current)
 *   - onMerge(targetUiId, resultingName)
 *   - onCancel()
 */
export default function MergePanel({
  currentTeam,
  candidateTeams,
  onMerge,
  onCancel,
}) {
  const { t } = useTranslation()
  const [targetUiId, setTargetUiId] = useState(candidateTeams[0]?.uiId || '')
  const [resultingName, setResultingName] = useState(currentTeam.name)

  return (
    <div className="mt-2 rounded border border-blue-200 bg-blue-50 p-2">
      <label className="block text-xs text-gray-600">Hedef takım</label>
      <select
        className="mt-1 block w-full rounded border-gray-300 text-sm"
        value={targetUiId}
        onChange={(e) => setTargetUiId(e.target.value)}
      >
        {candidateTeams.map((team) => (
          <option key={team.uiId} value={team.uiId}>
            {team.name}
          </option>
        ))}
      </select>
      <label className="mt-2 block text-xs text-gray-600">
        Birleştirilmiş takım adı
      </label>
      <input
        type="text"
        className="mt-1 block w-full rounded border-gray-300 text-sm"
        value={resultingName}
        onChange={(e) => setResultingName(e.target.value)}
      />
      <div className="mt-2 flex justify-end gap-2">
        <button type="button" onClick={onCancel} className="text-xs">
          {t('onboarding.teams.saveConfirmBack')}
        </button>
        <button
          type="button"
          onClick={() => onMerge(targetUiId, resultingName.trim())}
          disabled={!targetUiId || !resultingName.trim()}
          className="rounded bg-blue-600 px-2 py-1 text-xs text-white disabled:opacity-50"
        >
          Birleştir
        </button>
      </div>
    </div>
  )
}
