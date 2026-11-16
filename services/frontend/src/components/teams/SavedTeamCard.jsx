import { X } from 'lucide-react'

/**
 * SavedTeamCard — read-only card for the Teams page (Phase 7 D-19).
 *
 * Renders the team name + every member fully expanded inline (no drill-in
 * detail page in v1.1 per D-19). Each member shows name + optional title +
 * email + × delete button (D-23 / PRIVACY-02).
 *
 * The card is intentionally minimal: no edit affordances (no rename / merge /
 * split / overflow menu) because edit operations only exist on the onboarding
 * edit page per D-23 ("Deleting a team is NOT a Teams-page action in v1.1").
 *
 * Props:
 *   - team: { id, name, members: [{ id, name, title?, email }] }
 *   - onDeleteMember(memberId, memberSnapshot): parent handles the API call +
 *     5s undo toast. The snapshot is passed back so the parent can restore
 *     the chip locally on undo without re-fetching from the server.
 */
export default function SavedTeamCard({ team, onDeleteMember }) {
  return (
    <div className="rounded-lg border border-gray-200 bg-white p-5 shadow-sm">
      <h2 className="text-lg font-semibold">{team.name}</h2>
      <div className="mt-3 space-y-2">
        {team.members && team.members.length > 0 ? (
          team.members.map((m) => (
            <div
              key={m.id}
              className="flex items-center gap-3 rounded border border-gray-100 p-2"
            >
              <div className="flex-1">
                <div className="text-sm font-medium">{m.name}</div>
                {m.title && (
                  <div className="text-xs text-gray-500">{m.title}</div>
                )}
                <div className="text-xs text-gray-400">{m.email}</div>
              </div>
              <button
                type="button"
                onClick={() => onDeleteMember?.(m.id, m)}
                aria-label={`remove member ${m.email}`}
                className="text-gray-400 hover:text-red-500"
              >
                <X size={16} />
              </button>
            </div>
          ))
        ) : (
          <p className="text-xs text-gray-400">—</p>
        )}
      </div>
    </div>
  )
}
