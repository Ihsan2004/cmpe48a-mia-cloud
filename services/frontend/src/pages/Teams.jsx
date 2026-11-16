import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { showError } from '../utils/toast'
import { listTeams, detectTeams, deleteTeamMember, saveTeams } from '../services/api'
import SavedTeamCard from '../components/teams/SavedTeamCard'

/**
 * Teams — the v1.1 read-only Teams page (TEAMS-05 / TEAMS-06).
 *
 * Header layout: page title + ALWAYS-VISIBLE "Yeniden tara" button top-right
 * (D-20, the P6 "skip → user trapped" mitigation — the re-detect path is
 * never hidden behind the empty-state).
 *
 * Body: spacious card grid (D-19) or empty-state (D-21).
 *
 * Member × delete (D-23, PRIVACY-02): immediate API call + optimistic local
 * removal + 5s sonner undo toast. "Geri al" restores the chip locally; we
 * intentionally do NOT issue a re-add API call on undo in v1.1 because there
 * is no AddMember endpoint on the Teams page surface (would require a new
 * backend route). The toast is therefore primarily a CONFIRMATION + UNDO-OF-
 * UI; the user must re-add the member via the onboarding flow's "Yeniden
 * tara" path if they need them back permanently. This is an acceptable v1.1
 * trade-off per D-23 (single-action undo, simple model).
 *
 * Detect 409 DETECT_RUNNING (RESEARCH Pitfall 4): silent — the button is
 * already disabled while a request is in flight; second clicks are dropped
 * by the disabled state, but if a separate tab fires detect first, we treat
 * the 409 as a no-op.
 */
export default function Teams() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const [teams, setTeams] = useState([])
  const [loading, setLoading] = useState(true)
  const [isDetecting, setIsDetecting] = useState(false)
  // Pending suggestions surfaced from the most recent detect run. Each
  // entry: { tempId, name, members: [{email, shared_meeting_count}],
  //          confidence_bucket, savingState: 'idle'|'saving' }.
  // We render these inline above the saved-team grid instead of bouncing
  // through /onboarding/teams (OnboardingGuard would redirect onboarded
  // users right back here, swallowing the result).
  const [suggestions, setSuggestions] = useState([])

  const refresh = async () => {
    setLoading(true)
    try {
      const data = await listTeams()
      setTeams(data?.teams || [])
    } catch (err) {
      showError(t('teams.errors.saveFailed'))
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    refresh()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const handleRedetect = async () => {
    if (isDetecting) return
    setIsDetecting(true)
    try {
      // D-20: surface a quiet confirmation toast first.
      toast(t('teams.redetectConfirmToast'))
      const result = await detectTeams()

      // Degenerate path: explain WHY using empty_reason (issue: previous code
      // discarded the response and bounced through OnboardingGuard, leaving
      // the user with a generic "Henüz takımın yok" and no idea what went
      // wrong).
      if (result?.status === 'degenerate') {
        const reason = result.empty_reason || 'fallback'
        const key = `teams.empty_reason.${reason}`
        const message = t(key, { defaultValue: t('teams.empty_reason.fallback') })
        showError(message)
        return
      }

      // Success path: render the proposals inline on /teams so onboarded
      // users can review + save without bouncing through OnboardingGuard.
      const teamCount = Array.isArray(result?.teams) ? result.teams.length : 0
      if (teamCount > 0) {
        toast(t('teams.redetectFoundTeams', { count: teamCount }))
        setSuggestions(
          result.teams.map((sug, i) => ({
            tempId: `sug-${Date.now()}-${i}`,
            name: sug.suggested_name || '',
            members: Array.isArray(sug.members) ? sug.members : [],
            confidence_bucket: sug.confidence_bucket || '',
            savingState: 'idle',
          })),
        )
        return
      }

      // Status was neither degenerate nor success-with-teams (e.g. running,
      // or empty success — defensive fallback).
      showError(t('teams.empty_reason.fallback'))
    } catch (err) {
      if (err?.code === 'DETECT_RUNNING') {
        // Pitfall 4 — silent no-op.
      } else if (err?.code === 'GOOGLE_AUTH_EXPIRED') {
        showError('Google bağlantısı yenilenmeli')
        navigate('/connect-calendar', { replace: true })
      } else {
        showError(t('teams.errors.fetchFailed'))
      }
    } finally {
      setIsDetecting(false)
    }
  }

  const handleDeleteMember = async (memberId, snapshot) => {
    // Optimistic removal — drop the member from local state immediately.
    setTeams((prev) =>
      prev.map((tm) => ({
        ...tm,
        members: (tm.members || []).filter((m) => m.id !== memberId),
      })),
    )

    // Fire the API call (best-effort). 404 → already gone, no error.
    try {
      await deleteTeamMember(memberId)
    } catch (err) {
      if (err?.status !== 404) {
        showError(t('teams.errors.saveFailed'))
        // Restore on hard error.
        setTeams((prev) =>
          prev.map((tm) =>
            tm.id === snapshot.team_id
              ? { ...tm, members: [...(tm.members || []), snapshot] }
              : tm,
          ),
        )
        return
      }
    }

    // D-23: 5s sonner undo toast — UI-only restore (no API re-add).
    toast(t('teams.deleteMemberConfirm'), {
      duration: 5000,
      action: {
        label: t('teams.deleteMemberUndo'),
        onClick: () => {
          setTeams((prev) =>
            prev.map((tm) =>
              tm.id === snapshot.team_id
                ? { ...tm, members: [...(tm.members || []), snapshot] }
                : tm,
            ),
          )
        },
      },
    })
  }

  // SaveTeam payload requires { name, members:[{name, email}] }. Members
  // returned by the detector only carry email; derive the display name
  // from the local part so the backend's NAME_REQUIRED validation passes.
  // The user can rename members later via the saved team card.
  const memberNameFromEmail = (email) => {
    const local = String(email || '').split('@')[0] || ''
    return local.replace(/[._-]+/g, ' ').trim() || email || 'Üye'
  }

  const handleSaveSuggestion = async (tempId, editedName) => {
    const sug = suggestions.find((s) => s.tempId === tempId)
    if (!sug) return

    const name = String(editedName || sug.name || '').trim()
    if (!name) {
      showError(t('teams.errors.saveFailed'))
      return
    }

    setSuggestions((prev) =>
      prev.map((s) =>
        s.tempId === tempId ? { ...s, savingState: 'saving' } : s,
      ),
    )

    try {
      await saveTeams({
        teams: [
          {
            name,
            members: sug.members.map((m) => ({
              name: memberNameFromEmail(m.email),
              email: m.email,
            })),
          },
        ],
      })
      toast(t('teams.suggestionsSaveSuccess', { name }))
      setSuggestions((prev) => prev.filter((s) => s.tempId !== tempId))
      refresh()
    } catch (err) {
      setSuggestions((prev) =>
        prev.map((s) =>
          s.tempId === tempId ? { ...s, savingState: 'idle' } : s,
        ),
      )
      if (
        err?.code === 'DUPLICATE_TEAM_NAME' ||
        err?.code === 'DUPLICATE_MEMBER_EMAIL' ||
        err?.code === 'DUPLICATE'
      ) {
        showError(t('teams.suggestionsSaveDuplicate'))
      } else if (err?.code === 'ALREADY_COMPLETED') {
        // Defensive — the v1.1 additive save change should make this
        // impossible, but keep a clear toast in case of stale backend.
        showError(t('teams.errors.alreadySaved'))
      } else {
        showError(t('teams.suggestionsSaveFailed'))
      }
    }
  }

  const handleDiscardSuggestion = (tempId) => {
    setSuggestions((prev) => prev.filter((s) => s.tempId !== tempId))
  }

  return (
    <div className="mx-auto max-w-4xl p-4">
      <header className="flex items-center justify-between">
        <h1 className="text-2xl font-semibold">{t('teams.pageTitle')}</h1>
        {/* P6 mitigation: ALWAYS visible — not just empty-state. D-20. */}
        <button
          type="button"
          onClick={handleRedetect}
          disabled={isDetecting}
          className="rounded bg-blue-600 px-3 py-1.5 text-sm text-white disabled:opacity-50"
          data-testid="redetect-button"
        >
          {isDetecting ? '…' : t('teams.redetect')}
        </button>
      </header>

      {suggestions.length > 0 && (
        <SuggestionsPanel
          suggestions={suggestions}
          onSave={handleSaveSuggestion}
          onDiscard={handleDiscardSuggestion}
          t={t}
        />
      )}

      {loading ? (
        <p className="mt-6 text-sm text-gray-500">Yükleniyor…</p>
      ) : teams.length === 0 ? (
        <div className="mt-10 rounded border border-dashed p-8 text-center">
          <p className="text-lg">{t('teams.emptyTitle')}</p>
          <button
            type="button"
            onClick={handleRedetect}
            disabled={isDetecting}
            className="mt-3 rounded bg-blue-600 px-3 py-1.5 text-sm text-white disabled:opacity-50"
          >
            {t('teams.emptyCta')}
          </button>
        </div>
      ) : (
        <div className="mt-6 grid grid-cols-1 gap-4 md:grid-cols-2">
          {teams.map((team) => (
            <SavedTeamCard
              key={team.id}
              team={team}
              onDeleteMember={(memberId, snapshot) =>
                handleDeleteMember(memberId, snapshot)
              }
            />
          ))}
        </div>
      )}

    </div>
  )
}

// SuggestionsPanel renders proposed teams returned by the latest detect
// run (status === 'success' with non-empty teams). Each card is its own
// state-island so per-suggestion saving + per-suggestion rename don't
// re-render the others.
function SuggestionsPanel({ suggestions, onSave, onDiscard, t }) {
  return (
    <section className="mt-6 rounded-lg border border-blue-200 bg-blue-50 p-4">
      <header className="mb-3">
        <h2 className="text-lg font-semibold">
          {t('teams.suggestionsTitle')}
        </h2>
        <p className="text-sm text-gray-600">
          {t('teams.suggestionsSubtitle')}
        </p>
      </header>
      <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
        {suggestions.map((s) => (
          <SuggestionCard
            key={s.tempId}
            suggestion={s}
            onSave={onSave}
            onDiscard={onDiscard}
            t={t}
          />
        ))}
      </div>
    </section>
  )
}

function SuggestionCard({ suggestion, onSave, onDiscard, t }) {
  const [name, setName] = useState(suggestion.name || '')
  const isSaving = suggestion.savingState === 'saving'

  const confidenceKey =
    suggestion.confidence_bucket === 'high'
      ? 'teams.confidence.high'
      : suggestion.confidence_bucket === 'low'
        ? 'teams.confidence.low'
        : 'teams.confidence.mid'

  return (
    <div className="rounded border border-gray-200 bg-white p-3">
      <div className="mb-2 flex items-start justify-between gap-2">
        <input
          type="text"
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder={t('teams.suggestionsTeamFallbackName')}
          className="min-w-0 flex-1 rounded border border-gray-300 px-2 py-1 text-sm"
          disabled={isSaving}
          aria-label="team name"
        />
        {suggestion.confidence_bucket && (
          <span className="shrink-0 rounded bg-gray-100 px-2 py-1 text-xs text-gray-700">
            {t(confidenceKey)}
          </span>
        )}
      </div>

      <div className="mb-3 text-xs text-gray-500">
        {t('teams.suggestionsMemberCount', {
          count: suggestion.members.length,
        })}
      </div>

      <div className="mb-3 flex flex-wrap gap-1">
        {suggestion.members.map((m) => (
          <span
            key={m.email}
            className="rounded bg-gray-100 px-2 py-0.5 text-xs font-mono"
          >
            {m.email}
          </span>
        ))}
      </div>

      <div className="flex gap-2">
        <button
          type="button"
          onClick={() => onSave(suggestion.tempId, name)}
          disabled={isSaving || !name.trim()}
          className="rounded bg-blue-600 px-3 py-1.5 text-sm text-white disabled:opacity-50"
        >
          {isSaving
            ? t('teams.suggestionsSaving')
            : t('teams.suggestionsSave')}
        </button>
        <button
          type="button"
          onClick={() => onDiscard(suggestion.tempId)}
          disabled={isSaving}
          className="rounded border border-gray-300 bg-white px-3 py-1.5 text-sm text-gray-700 disabled:opacity-50"
        >
          {t('teams.suggestionsDiscard')}
        </button>
      </div>
    </div>
  )
}
