import { createContext, useContext } from 'react'

/**
 * UserContext exposes the App.jsx-fetched user object (which includes
 * teams_onboarding_status) to descendants without prop drilling.
 *
 * The default value is null — children should always read from the
 * provider wrapping the protected tree in App.jsx.
 */
export const UserContext = createContext(null)

/**
 * useUser is a convenience hook for components that need the full user
 * object (e.g., Sidebar reads .teams_onboarding_status from it via the
 * useOnboardingStatus wrapper).
 */
export function useUser() {
  return useContext(UserContext)
}
