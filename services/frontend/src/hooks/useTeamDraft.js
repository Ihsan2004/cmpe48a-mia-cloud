import { useEffect, useRef, useState, useCallback } from 'react'

const STORAGE_KEY = 'team-onboarding-draft-v1' // D-33 versioned for future forced-clears
const DEBOUNCE_MS = 200 // D-34 debounced writes

/**
 * useTeamDraft — sessionStorage-backed edit-state hook for the onboarding
 * page. Owns the entire D-33/D-34/D-35/D-36 lifecycle:
 *
 *   - D-33: storage key 'team-onboarding-draft-v1'
 *   - D-34: 200ms debounced writes on every state mutation
 *   - D-35: caller decides when to hydrate (see hasDraft return)
 *   - D-36: clearDraft() removes the key (called on save-success / skip-success
 *           / user-confirms-redetect-overwrite)
 *
 * @param {Object} initial — the initial state when no draft exists
 * @returns {{ state, setState, clearDraft, hasDraft }}
 */
export function useTeamDraft(initial) {
  // hasDraft is computed ONCE at mount — it reflects the initial
  // sessionStorage state, not the live state. Caller uses this to decide
  // whether to hydrate or fetch fresh.
  const hadDraftAtMount = useRef(false)

  const [state, setStateRaw] = useState(() => {
    try {
      const raw = sessionStorage.getItem(STORAGE_KEY)
      if (raw == null) return initial
      const parsed = JSON.parse(raw)
      hadDraftAtMount.current = true
      return parsed
    } catch {
      // Corrupt sessionStorage — treat as no draft.
      return initial
    }
  })

  const debounceTimer = useRef(null)

  const setState = useCallback((updater) => {
    setStateRaw((prev) => {
      const next = typeof updater === 'function' ? updater(prev) : updater

      // Debounce the sessionStorage write — multiple rapid mutations
      // result in ONE write with the latest value (D-34).
      if (debounceTimer.current) clearTimeout(debounceTimer.current)
      debounceTimer.current = setTimeout(() => {
        try {
          sessionStorage.setItem(STORAGE_KEY, JSON.stringify(next))
        } catch {
          // Quota exceeded or other storage failure — best-effort only.
        }
      }, DEBOUNCE_MS)

      return next
    })
  }, [])

  const clearDraft = useCallback(() => {
    if (debounceTimer.current) {
      clearTimeout(debounceTimer.current)
      debounceTimer.current = null
    }
    try {
      sessionStorage.removeItem(STORAGE_KEY)
    } catch {
      // ignore
    }
  }, [])

  // Cleanup pending debounce on unmount.
  useEffect(() => {
    return () => {
      if (debounceTimer.current) clearTimeout(debounceTimer.current)
    }
  }, [])

  return { state, setState, clearDraft, hasDraft: hadDraftAtMount.current }
}
