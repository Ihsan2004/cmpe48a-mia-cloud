import { useState, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { MoreVertical } from 'lucide-react'
import { toast } from 'sonner'
import ConfidenceBadge from './ConfidenceBadge'
import WhyDisclosure from './WhyDisclosure'
import MemberChip from './MemberChip'
import MemberPicker from './MemberPicker'
import MergePanel from './MergePanel'
import SplitPanel from './SplitPanel'

/**
 * TeamCard — direct-manipulation team card for the edit page.
 *
 * Props are the union of every D-* interaction:
 *   - team: full edit-state team
 *   - candidateMembers: array of {email, name, sharedMeetingCount} from other
 *                       teams (for D-04 add-existing)
 *   - candidateTeams: array of {uiId, name} (other teams, for D-07 merge)
 *   - onRename(newName)
 *   - onRemoveMember(memberUiId)
 *   - onUndoRemoveMember(memberSnapshot)
 *   - onAddExistingMember(memberPayload)
 *   - onAddExternalMember({name, email})
 *   - onMemberNameChange(memberUiId, newName)
 *   - onMemberTitleChange(memberUiId, newTitle)
 *   - onMerge(targetTeamUiId, resultingName)
 *   - onSplit(checkedMemberUiIds, newTeamName)
 *   - onDeleteTeam()
 *   - onUndoDeleteTeam(teamSnapshot)
 *
 * Internally orchestrates: rename edit mode, overflow menu open/close,
 * picker open/close, merge-panel open/close, split-panel open/close, and
 * the 5s sonner undo toasts for member-remove and team-delete.
 */
export default function TeamCard({
  team,
  candidateMembers,
  candidateTeams,
  onRename,
  onRemoveMember,
  onUndoRemoveMember,
  onAddExistingMember,
  onAddExternalMember,
  onMemberNameChange,
  onMemberTitleChange,
  onMerge,
  onSplit,
  onDeleteTeam,
  onUndoDeleteTeam,
}) {
  const { t } = useTranslation()
  const [editingName, setEditingName] = useState(false)
  const [nameDraft, setNameDraft] = useState(team.name)
  const [menuOpen, setMenuOpen] = useState(false)
  const [pickerOpen, setPickerOpen] = useState(false)
  const [mergeOpen, setMergeOpen] = useState(false)
  const [splitOpen, setSplitOpen] = useState(false)
  const menuRef = useRef(null)

  const commitName = () => {
    setEditingName(false)
    if (nameDraft.trim() && nameDraft.trim() !== team.name) {
      onRename(nameDraft.trim())
    } else {
      setNameDraft(team.name)
    }
  }

  const handleRemoveMember = (memberUiId, memberSnapshot) => {
    onRemoveMember(memberUiId)
    // D-03 5s sonner undo toast.
    toast(t('onboarding.teams.memberRemoved'), {
      duration: 5000,
      action: {
        label: t('onboarding.teams.undo'),
        onClick: () => onUndoRemoveMember?.(memberSnapshot),
      },
    })
  }

  const handleDeleteTeam = () => {
    setMenuOpen(false)
    const snapshot = team
    onDeleteTeam()
    // D-09 5s sonner undo toast.
    toast(t('onboarding.teams.teamDeleted'), {
      duration: 5000,
      action: {
        label: t('onboarding.teams.undo'),
        onClick: () => onUndoDeleteTeam?.(snapshot),
      },
    })
  }

  return (
    <div className="rounded-lg border border-gray-200 bg-white p-4">
      <div className="flex items-center gap-2">
        <div className="flex-1">
          {editingName ? (
            <input
              autoFocus
              type="text"
              value={nameDraft}
              onChange={(e) => setNameDraft(e.target.value)}
              onBlur={commitName}
              onKeyDown={(e) => {
                if (e.key === 'Enter') {
                  e.preventDefault()
                  commitName()
                }
                if (e.key === 'Escape') {
                  e.preventDefault()
                  setNameDraft(team.name)
                  setEditingName(false)
                }
              }}
              className="border-0 border-b text-lg font-medium focus:border-blue-500 focus:outline-none"
              aria-label="team name"
            />
          ) : (
            <button
              type="button"
              onClick={() => setEditingName(true)}
              className="text-lg font-medium hover:underline"
            >
              {team.name}
            </button>
          )}
        </div>
        <ConfidenceBadge bucket={team.confidence_bucket} />
        <div className="relative" ref={menuRef}>
          <button
            type="button"
            onClick={() => setMenuOpen((o) => !o)}
            aria-label="team menu"
            className="rounded p-1 hover:bg-gray-100"
          >
            <MoreVertical size={16} />
          </button>
          {menuOpen && (
            <div className="absolute right-0 z-10 mt-1 w-40 rounded border bg-white shadow-md">
              <button
                type="button"
                onClick={() => {
                  setEditingName(true)
                  setMenuOpen(false)
                }}
                className="block w-full px-3 py-1.5 text-left text-sm hover:bg-gray-100"
              >
                {t('onboarding.teams.renameTeam')}
              </button>
              <button
                type="button"
                onClick={() => {
                  setMergeOpen(true)
                  setMenuOpen(false)
                }}
                className="block w-full px-3 py-1.5 text-left text-sm hover:bg-gray-100"
              >
                {t('onboarding.teams.mergeTeam')}
              </button>
              <button
                type="button"
                onClick={() => {
                  setSplitOpen(true)
                  setMenuOpen(false)
                }}
                className="block w-full px-3 py-1.5 text-left text-sm hover:bg-gray-100"
              >
                {t('onboarding.teams.splitTeam')}
              </button>
              <button
                type="button"
                onClick={handleDeleteTeam}
                className="block w-full px-3 py-1.5 text-left text-sm text-red-600 hover:bg-red-50"
              >
                {t('onboarding.teams.deleteTeam')}
              </button>
            </div>
          )}
        </div>
      </div>

      <div className="mt-3 space-y-2">
        {team.members.map((m) => (
          <MemberChip
            key={m.uiId}
            name={m.name}
            title={m.title}
            email={m.email}
            sharedMeetingCount={m.shared_meeting_count}
            onRemove={() => handleRemoveMember(m.uiId, m)}
            onNameChange={(v) => onMemberNameChange(m.uiId, v)}
            onTitleChange={(v) => onMemberTitleChange(m.uiId, v)}
          />
        ))}
      </div>

      <div className="mt-2">
        {!pickerOpen ? (
          <button
            type="button"
            onClick={() => setPickerOpen(true)}
            className="text-sm text-blue-600 hover:underline"
          >
            {t('onboarding.teams.addExistingPerson')}
          </button>
        ) : (
          <MemberPicker
            availableMembers={candidateMembers}
            onAddExisting={(m) => {
              onAddExistingMember(m)
              setPickerOpen(false)
            }}
            onAddExternal={(m) => {
              onAddExternalMember(m)
              setPickerOpen(false)
            }}
            onCancel={() => setPickerOpen(false)}
          />
        )}
      </div>

      {mergeOpen && (
        <MergePanel
          currentTeam={team}
          candidateTeams={candidateTeams}
          onMerge={(target, name) => {
            onMerge(target, name)
            setMergeOpen(false)
          }}
          onCancel={() => setMergeOpen(false)}
        />
      )}

      {splitOpen && (
        <SplitPanel
          members={team.members}
          onSplit={(ids, name) => {
            onSplit(ids, name)
            setSplitOpen(false)
          }}
          onCancel={() => setSplitOpen(false)}
        />
      )}

      <WhyDisclosure topSeries={team.top_series} />
    </div>
  )
}
