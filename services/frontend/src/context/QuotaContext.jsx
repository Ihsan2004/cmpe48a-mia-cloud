import { createContext, useCallback, useContext, useEffect, useRef, useState } from 'react'
import { getMyQuota } from '../services/api'

// QuotaContext owns the single source of truth for quota UI surfaces:
// the >=80% banner, the 100% modal, and the /settings/usage page.
//
// Polling cadence: 60s passive refresh while authenticated. Push
// updates: window 'quota:exceeded' CustomEvent fired from authFetch
// when ANY endpoint returns 429 + body.error='quota_exceeded'. The
// event handler optimistically flips used_pct to 100 + Exceeded so the
// modal pops immediately, then we re-fetch to settle on authoritative
// state (cycle_resets_at, tier badge).
//
// Backend response shape:
//   { tier, used_pct, cycle_starts_at, cycle_resets_at }
// No dollar fields ever — kullanıcı dolar görmez (Pivot UX kararı).

const QuotaContext = createContext(null)

const POLL_INTERVAL_MS = 60_000
const REFETCH_AFTER_429_MS = 1_500 // brief delay so backend counter UPSERT lands first

export function QuotaProvider({ authenticated, children }) {
  const [status, setStatus] = useState(null) // null while loading, then {tier, used_pct, ...}
  const [loading, setLoading] = useState(authenticated)
  const [error, setError] = useState(null)
  // Modal state lives in context (not component-local) so any 429 from
  // anywhere in the app triggers the same modal — no per-route wiring.
  const [exceededAt, setExceededAt] = useState(null) // timestamp; null = no modal
  const refetchTimer = useRef(null)

  const refetch = useCallback(async () => {
    try {
      const data = await getMyQuota()
      setStatus(data)
      setError(null)
    } catch (err) {
      setError(err)
      // Keep stale status visible rather than blanking out.
    } finally {
      setLoading(false)
    }
  }, [])

  // Initial fetch + polling. Skip entirely when unauthenticated so the
  // login page doesn't fire 401s in the background.
  useEffect(() => {
    if (!authenticated) {
      setStatus(null)
      setLoading(false)
      return
    }
    refetch()
    const id = setInterval(refetch, POLL_INTERVAL_MS)
    return () => clearInterval(id)
  }, [authenticated, refetch])

  // Listen for global 429s. Optimistic flip + delayed authoritative
  // refetch lets the modal pop in the same tick as the failed request,
  // even before /usage/me confirms cycle_resets_at.
  useEffect(() => {
    if (!authenticated) return

    const handler = (event) => {
      const detail = event.detail || {}
      setStatus((prev) => ({
        tier: detail.tier || prev?.tier || 'free',
        used_pct: typeof detail.used_pct === 'number' ? detail.used_pct : 100,
        cycle_starts_at: prev?.cycle_starts_at ?? null,
        cycle_resets_at: detail.cycle_resets_at || prev?.cycle_resets_at || null,
      }))
      setExceededAt(Date.now())

      if (refetchTimer.current) clearTimeout(refetchTimer.current)
      refetchTimer.current = setTimeout(refetch, REFETCH_AFTER_429_MS)
    }

    window.addEventListener('quota:exceeded', handler)
    return () => {
      window.removeEventListener('quota:exceeded', handler)
      if (refetchTimer.current) clearTimeout(refetchTimer.current)
    }
  }, [authenticated, refetch])

  const dismissModal = useCallback(() => setExceededAt(null), [])

  const value = {
    status,
    loading,
    error,
    exceededAt,
    isUnlimited: status?.tier === 'internal',
    refetch,
    dismissModal,
  }

  return <QuotaContext.Provider value={value}>{children}</QuotaContext.Provider>
}

export function useQuota() {
  const ctx = useContext(QuotaContext)
  if (!ctx) {
    throw new Error('useQuota must be used inside <QuotaProvider>')
  }
  return ctx
}
