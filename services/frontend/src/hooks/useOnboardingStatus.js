import { useUser } from '../contexts/UserContext'

/**
 * useOnboardingStatus is the canonical read of teams_onboarding_status
 * for route guards and any feature page that needs to branch on
 * onboarding state.
 *
 * Returns:
 *   - status: 'pending' | 'completed' | 'skipped' | 'empty_state_skipped' | null (during initial fetch)
 *   - loading: true while App.jsx is still resolving its initial profile fetch
 *   - refresh: stub for plan 07-04 (which may need to re-read status after save/skip)
 *
 * Design note (RESEARCH §Q6): we deliberately do NOT fire a second
 * profile fetch here. The existing App.jsx boot fetch is the single
 * source of truth; this hook is a thin read of the resulting Context.
 */
export function useOnboardingStatus() {
  const user = useUser()
  return {
    status: user?.teams_onboarding_status ?? null,
    loading: user === null,
    // Plan 07-04 may extend this with a callback that triggers App.jsx
    // to re-fetch profile (e.g., after save flips status server-side).
    refresh: () => {},
  }
}
