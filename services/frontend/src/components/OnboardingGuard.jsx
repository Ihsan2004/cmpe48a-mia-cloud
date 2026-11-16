import { Navigate } from 'react-router-dom'
import { useOnboardingStatus } from '../hooks/useOnboardingStatus'

/**
 * OnboardingGuard implements CONTEXT D-28/D-29 status-gating.
 *
 * Usage:
 *   <OnboardingGuard mode="onboarding">     // for /onboarding/teams
 *     <OnboardingTeams />
 *   </OnboardingGuard>
 *
 *   <OnboardingGuard mode="teams">           // for /teams
 *     <Teams />
 *   </OnboardingGuard>
 *
 * Rules:
 *   mode='onboarding': pass-through when status==='pending';
 *     redirect to /teams when status is any finalized value;
 *     return null while loading.
 *   mode='teams': pass-through when status is finalized;
 *     redirect to /onboarding/teams when status==='pending';
 *     return null while loading.
 *
 * Composes ALONGSIDE ProtectedRoute — does not replace it. Caller wraps
 * <ProtectedRoute><OnboardingGuard mode=…><Page/></OnboardingGuard></ProtectedRoute>.
 *
 * Silent redirects (no error screen) per D-29.
 */
export default function OnboardingGuard({ mode, children }) {
  const { status, loading } = useOnboardingStatus()

  if (loading) return null

  if (mode === 'onboarding') {
    if (status !== 'pending') {
      // D-29: deep-link to /onboarding/teams with a finalized status →
      // silent redirect to /teams.
      return <Navigate to="/teams" replace />
    }
    return children
  }

  if (mode === 'teams') {
    if (status === 'pending') {
      return <Navigate to="/onboarding/teams" replace />
    }
    return children
  }

  // Defensive: unknown mode → render children (fail-open is safer than
  // accidentally blocking the user).
  return children
}
