import { useState } from 'react'
import { useTranslation } from 'react-i18next'

/**
 * MemberPicker — combobox for D-04 (add existing) + D-05 (add external).
 *
 * Props:
 *   - availableMembers: [{ email, name, sharedMeetingCount }] — union of all
 *                       other teams' members not already in this team
 *   - onAddExisting(member): called when user clicks an existing entry
 *   - onAddExternal({ name, email }): called when user submits the external-add row
 *   - onCancel(): dismiss the picker
 */
export default function MemberPicker({
  availableMembers,
  onAddExisting,
  onAddExternal,
  onCancel,
}) {
  const { t } = useTranslation()
  const [query, setQuery] = useState('')

  const filtered = (availableMembers || []).filter((m) => {
    const q = query.toLowerCase()
    return (
      m.email.toLowerCase().includes(q) || (m.name || '').toLowerCase().includes(q)
    )
  })

  // D-05 external-add row: surface when query is non-empty AND parseable as
  // "Name, email@host" (or simply contains an @).
  const externalCandidate = parseExternalQuery(query)

  return (
    <div className="rounded border border-gray-200 p-2">
      <input
        type="text"
        autoFocus
        value={query}
        onChange={(e) => setQuery(e.target.value)}
        placeholder="email veya 'isim, e-posta'"
        className="w-full border-0 p-1 text-sm focus:outline-none"
        aria-label="member picker query"
      />
      <ul className="mt-1 max-h-40 overflow-y-auto">
        {filtered.map((m) => (
          <li key={m.email}>
            <button
              type="button"
              onClick={() => onAddExisting(m)}
              className="w-full rounded px-2 py-1 text-left text-sm hover:bg-gray-100"
            >
              <span className="font-medium">
                {m.name || m.email.split('@')[0]}
              </span>
              <span className="ml-2 text-xs text-gray-500">{m.email}</span>
            </button>
          </li>
        ))}
        {filtered.length === 0 && query && !externalCandidate && (
          <li className="px-2 py-1 text-xs text-gray-500">Sonuç yok.</li>
        )}
        {externalCandidate &&
          !filtered.some(
            (m) =>
              m.email.toLowerCase() === externalCandidate.email.toLowerCase(),
          ) && (
            <li>
              <button
                type="button"
                onClick={() => onAddExternal(externalCandidate)}
                className="w-full rounded px-2 py-1 text-left text-sm text-blue-600 hover:bg-blue-50"
              >
                {t('onboarding.teams.addExternalRow', {
                  name: externalCandidate.name,
                  email: externalCandidate.email,
                })}
              </button>
            </li>
          )}
      </ul>
      <div className="mt-1 flex justify-end">
        <button
          type="button"
          onClick={onCancel}
          className="text-xs text-gray-500 hover:text-gray-700"
        >
          {t('onboarding.teams.saveConfirmBack')}
        </button>
      </div>
    </div>
  )
}

// parseExternalQuery accepts "Name, email@host" or a bare "email@host"
// (with name defaulting to local part). Returns null if neither matches.
function parseExternalQuery(q) {
  if (!q) return null
  const trimmed = q.trim()
  // "Name, email@host" form
  const m = trimmed.match(/^(.+?),\s*([^\s,]+@[^\s,]+)$/)
  if (m) {
    return { name: m[1].trim(), email: m[2].trim().toLowerCase() }
  }
  // bare email
  if (trimmed.includes('@') && !trimmed.includes(' ')) {
    return { name: trimmed.split('@')[0], email: trimmed.toLowerCase() }
  }
  return null
}
