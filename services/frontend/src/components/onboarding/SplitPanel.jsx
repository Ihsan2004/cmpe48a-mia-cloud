import { useState } from 'react'
import { useTranslation } from 'react-i18next'

/**
 * SplitPanel — D-08 inline split UI.
 *
 * Props:
 *   - members: [{ uiId, name, email }] — current team members
 *   - onSplit(checkedUiIds, newTeamName)
 *   - onCancel()
 */
export default function SplitPanel({ members, onSplit, onCancel }) {
  const { t } = useTranslation()
  const [checked, setChecked] = useState({})
  const [newName, setNewName] = useState('')

  const checkedIds = Object.keys(checked).filter((id) => checked[id])
  const canSubmit =
    checkedIds.length > 0 &&
    checkedIds.length < members.length &&
    newName.trim().length > 0

  return (
    <div className="mt-2 rounded border border-amber-200 bg-amber-50 p-2">
      <p className="text-xs text-gray-600">Yeni takıma taşınacak üyeleri seç:</p>
      <ul className="mt-1 max-h-40 overflow-y-auto">
        {members.map((m) => (
          <li key={m.uiId} className="flex items-center gap-2 text-sm">
            <input
              type="checkbox"
              checked={!!checked[m.uiId]}
              onChange={(e) =>
                setChecked({ ...checked, [m.uiId]: e.target.checked })
              }
              aria-label={`split member ${m.email}`}
            />
            <span>{m.name || m.email.split('@')[0]}</span>
            <span className="text-xs text-gray-400">{m.email}</span>
          </li>
        ))}
      </ul>
      <label className="mt-2 block text-xs text-gray-600">Yeni takım adı</label>
      <input
        type="text"
        className="mt-1 block w-full rounded border-gray-300 text-sm"
        value={newName}
        onChange={(e) => setNewName(e.target.value)}
      />
      <div className="mt-2 flex justify-end gap-2">
        <button type="button" onClick={onCancel} className="text-xs">
          {t('onboarding.teams.saveConfirmBack')}
        </button>
        <button
          type="button"
          onClick={() => onSplit(checkedIds, newName.trim())}
          disabled={!canSubmit}
          className="rounded bg-amber-600 px-2 py-1 text-xs text-white disabled:opacity-50"
        >
          Ayır
        </button>
      </div>
    </div>
  )
}
