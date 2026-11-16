import { useEffect, useState, useMemo } from 'react'
import { useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { showError, showSuccess } from '../utils/toast'
import { detectTeams, saveTeams, skipOnboarding } from '../services/api'
import { useTeamDraft } from '../hooks/useTeamDraft'
import StagedLoading from '../components/onboarding/StagedLoading'
import TeamCard from '../components/onboarding/TeamCard'
import SaveConfirmStrip from '../components/onboarding/SaveConfirmStrip'

const EMPTY_INITIAL = { teams: [] }

/**
 * OnboardingTeams — the v1.1 onboarding page (ONBOARD-01 + ONBOARD-04 +
 * ONBOARD-05 + EDIT-01..EDIT-11 + TRUST-02..TRUST-04 + PRIVACY-01).
 *
 * Three-state machine:
 *   1. mode='loading'  — staged Turkish copy via StagedLoading (D-30)
 *   2. mode='edit'     — direct-manipulation edit page (D-01..D-11)
 *   3. (terminal)      — navigated away on save/skip
 *
 * Hydration order at mount:
 *   - If useTeamDraft.hasDraft AND status==='pending' (D-35) → mode='edit',
 *     skip detect (the draft IS the source of truth)
 *   - Else → mode='loading', fire detectTeams(), then mode='edit'
 *
 * D-30 time-split: the loading screen advances from stage=1 to stage=2 at
 * min(600ms, detect-resolves), so even very-fast detects show both messages.
 */
export default function OnboardingTeams() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { state, setState, clearDraft, hasDraft } = useTeamDraft(EMPTY_INITIAL)

  const [mode, setMode] = useState(hasDraft ? 'edit' : 'loading')
  const [stage, setStage] = useState(1) // for StagedLoading
  const [isConfirming, setIsConfirming] = useState(false)
  const [isSaving, setIsSaving] = useState(false)
  const [saveDisabled, setSaveDisabled] = useState(false)

  // ---- detect lifecycle ----
  useEffect(() => {
    if (mode !== 'loading') return

    let cancelled = false
    const stageTimer = setTimeout(() => {
      if (!cancelled) setStage(2)
    }, 600)

    detectTeams()
      .then((dto) => {
        if (cancelled) return
        const hydrated = hydrateFromDTO(dto)
        setState(hydrated)
        setMode('edit')
      })
      .catch((err) => {
        if (cancelled) return
        switch (err?.code) {
          case 'GOOGLE_AUTH_EXPIRED':
            showError('Google bağlantısı yenilenmeli')
            navigate('/connect-calendar', { replace: true })
            break
          case 'FETCH_TRANSIENT':
          case 'FETCH_PERMANENT':
            showError(t('teams.errors.fetchFailed'))
            // Fall through to empty edit page so the user can skip / add manually.
            setMode('edit')
            setState({ teams: [] })
            break
          case 'DETECT_RUNNING':
            // RESEARCH Pitfall 4 — silent no-op. Stay on loading.
            break
          default:
            showError(t('teams.errors.fetchFailed'))
            setMode('edit')
            setState({ teams: [] })
        }
      })

    return () => {
      cancelled = true
      clearTimeout(stageTimer)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [mode])

  // ---- counts for SaveConfirmStrip ----
  const counts = useMemo(() => {
    const teams = state.teams.length
    const members = state.teams.reduce(
      (acc, team) => acc + team.members.length,
      0,
    )
    return { teams, members }
  }, [state])

  // ---- candidate members for D-04 add-existing (union of other teams) ----
  const candidateMembersByTeam = useMemo(() => {
    const map = {}
    state.teams.forEach((team) => {
      const others = []
      state.teams.forEach((otherTeam) => {
        if (otherTeam.uiId === team.uiId) return
        otherTeam.members.forEach((m) => {
          if (
            !team.members.some(
              (tm) => tm.email.toLowerCase() === m.email.toLowerCase(),
            )
          ) {
            others.push({
              email: m.email,
              name: m.name,
              sharedMeetingCount: m.shared_meeting_count,
            })
          }
        })
      })
      map[team.uiId] = others
    })
    return map
  }, [state])

  // ---- per-team mutations ----
  const mutateTeam = (uiId, updater) => {
    setState((prev) => ({
      ...prev,
      teams: prev.teams.map((tm) => (tm.uiId === uiId ? updater(tm) : tm)),
    }))
  }

  const onRename = (uiId, newName) =>
    mutateTeam(uiId, (team) => ({ ...team, name: newName }))
  const onRemoveMember = (uiId, memberUiId) =>
    mutateTeam(uiId, (team) => ({
      ...team,
      members: team.members.filter((m) => m.uiId !== memberUiId),
    }))
  const onUndoRemoveMember = (uiId, snapshot) =>
    mutateTeam(uiId, (team) => ({
      ...team,
      members: [...team.members, snapshot],
    }))
  const onAddExistingMember = (uiId, payload) =>
    mutateTeam(uiId, (team) => ({
      ...team,
      members: [
        ...team.members,
        {
          uiId: cryptoRandomId(),
          email: payload.email,
          name: payload.name || payload.email.split('@')[0],
          title: '',
          shared_meeting_count: payload.sharedMeetingCount ?? null,
        },
      ],
    }))
  const onAddExternalMember = (uiId, { name, email }) =>
    mutateTeam(uiId, (team) => ({
      ...team,
      members: [
        ...team.members,
        {
          uiId: cryptoRandomId(),
          email,
          name,
          title: '',
          shared_meeting_count: null,
        },
      ],
    }))
  const onMemberNameChange = (uiId, memberUiId, newName) =>
    mutateTeam(uiId, (team) => ({
      ...team,
      members: team.members.map((m) =>
        m.uiId === memberUiId ? { ...m, name: newName } : m,
      ),
    }))
  const onMemberTitleChange = (uiId, memberUiId, newTitle) =>
    mutateTeam(uiId, (team) => ({
      ...team,
      members: team.members.map((m) =>
        m.uiId === memberUiId ? { ...m, title: newTitle } : m,
      ),
    }))

  const onDeleteTeam = (uiId) => {
    setState((prev) => ({
      ...prev,
      teams: prev.teams.filter((team) => team.uiId !== uiId),
    }))
  }
  const onUndoDeleteTeam = (snapshot) => {
    setState((prev) => ({ ...prev, teams: [...prev.teams, snapshot] }))
  }

  const onMerge = (sourceUiId, targetUiId, resultingName) => {
    setState((prev) => {
      const source = prev.teams.find((team) => team.uiId === sourceUiId)
      const target = prev.teams.find((team) => team.uiId === targetUiId)
      if (!source || !target) return prev
      const mergedMembers = dedupeMembersByEmail([
        ...target.members,
        ...source.members,
      ])
      const newTeams = prev.teams
        .filter((team) => team.uiId !== sourceUiId)
        .map((team) =>
          team.uiId === targetUiId
            ? { ...team, name: resultingName, members: mergedMembers }
            : team,
        )
      return { ...prev, teams: newTeams }
    })
  }

  const onSplit = (sourceUiId, checkedUiIds, newTeamName) => {
    setState((prev) => {
      const source = prev.teams.find((team) => team.uiId === sourceUiId)
      if (!source) return prev
      const splitOut = source.members.filter((m) =>
        checkedUiIds.includes(m.uiId),
      )
      const remaining = source.members.filter(
        (m) => !checkedUiIds.includes(m.uiId),
      )
      const newTeam = {
        uiId: cryptoRandomId(),
        name: newTeamName,
        confidence_bucket: 'low',
        top_series: [],
        members: splitOut,
      }
      return {
        ...prev,
        teams: prev.teams
          .map((team) =>
            team.uiId === sourceUiId ? { ...team, members: remaining } : team,
          )
          .concat([newTeam]),
      }
    })
  }

  // ---- save / skip ----
  const handleSave = () => setIsConfirming(true)
  const handleBack = () => setIsConfirming(false)
  const handleConfirm = async () => {
    setIsSaving(true)
    try {
      const payload = {
        teams: state.teams.map((team) => ({
          name: team.name,
          members: team.members.map((m) => ({
            name: m.name,
            title: m.title || undefined,
            email: m.email,
          })),
        })),
      }
      await saveTeams(payload)
      clearDraft()
      showSuccess(t('teams.toast.saveSuccess'))
      navigate('/teams')
    } catch (err) {
      if (err?.code === 'ALREADY_COMPLETED') {
        showError(t('teams.errors.alreadySaved'))
        setSaveDisabled(true) // D-16: keep Kaydet disabled, stay on page
      } else {
        showError(t('teams.errors.saveFailed'))
      }
    } finally {
      setIsSaving(false)
    }
  }

  const handleSkip = async () => {
    const emptyState = state.teams.length === 0
    try {
      await skipOnboarding(emptyState)
      clearDraft()
      showSuccess(t('teams.toast.skipped'))
      navigate('/dashboard')
    } catch {
      showError(t('teams.errors.saveFailed'))
    }
  }

  // ---- empty-state CTA (EDIT-10) ----
  const handleAddEmptyTeam = () => {
    setState((prev) => ({
      ...prev,
      teams: [
        ...prev.teams,
        {
          uiId: cryptoRandomId(),
          name: 'Yeni takım',
          confidence_bucket: 'low',
          top_series: [],
          members: [],
        },
      ],
    }))
  }

  if (mode === 'loading') {
    return <StagedLoading stage={stage} onSkip={handleSkip} />
  }

  // mode === 'edit'
  return (
    <div className="mx-auto max-w-3xl p-4 pb-24">
      <h1 className="text-2xl font-semibold">{t('onboarding.teams.editTitle')}</h1>
      <p className="mt-1 text-sm text-gray-600">
        {t('onboarding.teams.editSubtitle')}
      </p>

      {state.teams.length === 0 ? (
        <div className="mt-6 rounded border border-dashed p-6 text-center">
          <p className="text-lg">{t('onboarding.teams.emptyStateTitle')}</p>
          <p className="mt-1 text-sm text-gray-600">
            {t('onboarding.teams.emptyStateBody')}
          </p>
          <button
            type="button"
            onClick={handleAddEmptyTeam}
            className="mt-3 rounded bg-blue-600 px-3 py-1.5 text-sm text-white"
          >
            {t('onboarding.teams.emptyStateCta')}
          </button>
        </div>
      ) : (
        <>
          <div className="mt-6">
            <button
              type="button"
              onClick={handleAddEmptyTeam}
              className="text-sm text-blue-600 hover:underline"
            >
              {t('onboarding.teams.emptyStateCta')}
            </button>
          </div>
          <div className="mt-2 space-y-3">
            {state.teams.map((team) => (
              <TeamCard
                key={team.uiId}
                team={team}
                candidateMembers={candidateMembersByTeam[team.uiId] || []}
                candidateTeams={state.teams
                  .filter((other) => other.uiId !== team.uiId)
                  .map((other) => ({ uiId: other.uiId, name: other.name }))}
                onRename={(n) => onRename(team.uiId, n)}
                onRemoveMember={(mUiId) => onRemoveMember(team.uiId, mUiId)}
                onUndoRemoveMember={(s) => onUndoRemoveMember(team.uiId, s)}
                onAddExistingMember={(p) => onAddExistingMember(team.uiId, p)}
                onAddExternalMember={(p) => onAddExternalMember(team.uiId, p)}
                onMemberNameChange={(mUiId, v) =>
                  onMemberNameChange(team.uiId, mUiId, v)
                }
                onMemberTitleChange={(mUiId, v) =>
                  onMemberTitleChange(team.uiId, mUiId, v)
                }
                onMerge={(target, name) => onMerge(team.uiId, target, name)}
                onSplit={(ids, name) => onSplit(team.uiId, ids, name)}
                onDeleteTeam={() => onDeleteTeam(team.uiId)}
                onUndoDeleteTeam={onUndoDeleteTeam}
              />
            ))}
          </div>
        </>
      )}

      {/* PRIVACY-01 + D-31 — Turkish KVKK disclosure in the edit page. */}
      <p className="mt-6 text-xs text-gray-500" data-testid="privacy-note">
        {t('onboarding.teams.privacyNote')}
      </p>

      <SaveConfirmStrip
        teamCount={counts.teams}
        memberCount={counts.members}
        isConfirming={isConfirming}
        isSaving={isSaving}
        saveDisabled={saveDisabled || isSaving}
        onSave={handleSave}
        onConfirm={handleConfirm}
        onBack={handleBack}
        onSkip={handleSkip}
      />
    </div>
  )
}

// ---- helpers ----

function hydrateFromDTO(dto) {
  if (!dto || !dto.teams) return { teams: [] }
  return {
    teams: dto.teams.map((team) => ({
      uiId: cryptoRandomId(),
      name: team.suggested_name || 'Takım',
      confidence_bucket: team.confidence_bucket,
      top_series: team.top_series || [],
      members: (team.members || []).map((m) => ({
        uiId: cryptoRandomId(),
        email: m.email,
        // RESEARCH Pitfall 5 — Phase 6 DTO carries email only; derive a default
        // display name from the local part. User can edit on the page.
        name: m.email.split('@')[0],
        title: '',
        shared_meeting_count: m.shared_meeting_count,
      })),
    })),
  }
}

function dedupeMembersByEmail(members) {
  const seen = new Set()
  return members.filter((m) => {
    const key = m.email.toLowerCase()
    if (seen.has(key)) return false
    seen.add(key)
    return true
  })
}

function cryptoRandomId() {
  if (typeof crypto !== 'undefined' && crypto.randomUUID) return crypto.randomUUID()
  return Math.random().toString(36).slice(2) + Date.now().toString(36)
}
