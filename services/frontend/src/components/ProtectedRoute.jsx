import { useEffect, useState } from 'react'
import { Navigate } from 'react-router-dom'
import { getCurrentUser, isAuthenticated, restoreDesktopAuthFromNative } from '../services/auth'
import { getConsentStatus } from '../services/consents'

export default function ProtectedRoute({ children, requiresConsents = true }) {
  // TEMP (dev): disable auth gate (bypass login screen).
  // Re-enable by setting this back to false.
  const AUTH_DISABLED = false
  if (AUTH_DISABLED) return children

  const [loading, setLoading] = useState(true)
  const [user, setUser] = useState(null)
  // null = unknown, true = ok to render children, false = redirect to gate.
  // We keep this separate from `user` so a transient consent-status
  // fetch failure doesn't accidentally invalidate the auth state.
  const [consentsOk, setConsentsOk] = useState(true)

  useEffect(() => {
    let cancelled = false

    const checkAuth = async () => {
      await restoreDesktopAuthFromNative()

      if (!isAuthenticated()) {
        if (!cancelled) setLoading(false)
        return
      }

      try {
        const currentUser = await getCurrentUser()
        if (cancelled) return
        setUser(currentUser)

        if (currentUser && requiresConsents) {
          try {
            const status = await getConsentStatus()
            if (cancelled) return
            // If the call returns null (e.g. no token, network blip),
            // be lenient and allow access — the gate page can be
            // reached manually if needed, and the next navigation
            // will re-check. We do NOT want to trap users out of
            // their own dashboard due to a flaky request.
            if (status && status.satisfied === false) {
              setConsentsOk(false)
            }
          } catch (err) {
            console.error('Consent status check failed', err)
            // Lenient on fetch error (see comment above).
          }
        }
      } catch (error) {
        console.error('Error checking auth:', error)
        if (!cancelled) setUser(null)
      } finally {
        if (!cancelled) setLoading(false)
      }
    }

    checkAuth()
    return () => {
      cancelled = true
    }
  }, [requiresConsents])

  if (loading) {
    return (
      <div className="min-h-screen bg-background flex items-center justify-center">
        <div className="text-text font-sans">Loading...</div>
      </div>
    )
  }

  if (!user) return <Navigate to="/login" replace />
  if (!consentsOk) return <Navigate to="/consents-pending" replace />
  return children
}
