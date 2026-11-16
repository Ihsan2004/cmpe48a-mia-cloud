import { useTranslation } from 'react-i18next'
import { X } from 'lucide-react'

/**
 * MemberChip — single member row inside a TeamCard.
 *
 * Props:
 *   - name, title, email, sharedMeetingCount — display + edit
 *   - onRemove(): remove this member (parent fires the 5s undo toast)
 *   - onNameChange(newName), onTitleChange(newTitle): inline edit handlers
 *
 * The name is editable inline. Title is an always-editable placeholder input
 * (EDIT-08, optional + empty default). Email is read-only (the only way to
 * change an email is delete + re-add).
 */
export default function MemberChip({
  name,
  title,
  email,
  sharedMeetingCount,
  onRemove,
  onNameChange,
  onTitleChange,
}) {
  const { t } = useTranslation()
  return (
    <div className="flex items-start gap-2 rounded border border-gray-200 p-2">
      <div className="flex-1">
        <input
          type="text"
          className="block w-full border-0 bg-transparent p-0 text-sm font-medium focus:outline-none"
          value={name}
          onChange={(e) => onNameChange?.(e.target.value)}
          aria-label="member name"
        />
        <input
          type="text"
          className="block w-full border-0 bg-transparent p-0 text-xs text-gray-500 focus:outline-none"
          placeholder={t('onboarding.teams.titlePlaceholder')}
          value={title || ''}
          onChange={(e) => onTitleChange?.(e.target.value)}
          aria-label="member title"
        />
        <div className="mt-1 text-xs text-gray-400">
          <span>{email}</span>
          {sharedMeetingCount != null && (
            <>
              {' · '}
              <span>
                {t('onboarding.teams.sharedMeetings', { count: sharedMeetingCount })}
              </span>
            </>
          )}
        </div>
      </div>
      <button
        type="button"
        onClick={onRemove}
        aria-label="remove member"
        className="text-gray-400 hover:text-red-500"
      >
        <X size={14} />
      </button>
    </div>
  )
}
