// Native authentication service - replaces Supabase Auth
// All authentication is now handled by the Go backend

const API_URL = import.meta.env.VITE_API_URL
if (!API_URL) {
  console.error('⚠️ VITE_API_URL is not set in environment variables')
}

// Store tokens in localStorage
const ACCESS_TOKEN_KEY = 'mia_access_token'
const REFRESH_TOKEN_KEY = 'mia_refresh_token'
const USER_KEY = 'mia_user'

// Get stored access token
export function getAccessToken() {
  return localStorage.getItem(ACCESS_TOKEN_KEY)
}

// Get stored refresh token
export function getRefreshToken() {
  return localStorage.getItem(REFRESH_TOKEN_KEY)
}

// Get stored user
export function getStoredUser() {
  const userStr = localStorage.getItem(USER_KEY)
  if (!userStr) return null
  try {
    return JSON.parse(userStr)
  } catch {
    return null
  }
}

// Store tokens and user
export function storeAuthData(accessToken, refreshToken, user) {
  localStorage.setItem(ACCESS_TOKEN_KEY, accessToken)
  localStorage.setItem(REFRESH_TOKEN_KEY, refreshToken)
  localStorage.setItem(USER_KEY, JSON.stringify(user))
}

// Store only user data
export function storeUser(user) {
  localStorage.setItem(USER_KEY, JSON.stringify(user))
}

// Clear auth data
export function clearAuthData() {
  localStorage.removeItem(ACCESS_TOKEN_KEY)
  localStorage.removeItem(REFRESH_TOKEN_KEY)
  localStorage.removeItem(USER_KEY)
  // Drop the consent status cache so a new user (after re-login)
  // doesn't see the previous user's gate state. Dynamic import to
  // avoid the auth.js ↔ consents.js circular import at module init.
  import('./consents').then(({ invalidateConsentStatusCache }) => {
    invalidateConsentStatusCache()
  }).catch(() => {
    // Best effort — bundler edge cases shouldn't break logout.
  })
}

// Sign up
//
// `consents` is required by the backend (since Phase 1.3). Pass the
// array produced by buildSignupConsents() — undefined/empty will 400.
export async function signUp(email, password, fullName = '', consents = []) {
  const response = await fetch(`${API_URL}/auth/signup`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({
      email,
      password,
      full_name: fullName,
      consents,
    }),
  })

  if (!response.ok) {
    const error = await response.json()
    throw new Error(error.error || 'Failed to sign up')
  }

  const data = await response.json()
  storeAuthData(data.access_token, data.refresh_token, data.user)
  return data
}

// Sign in
export async function signIn(email, password) {
  const response = await fetch(`${API_URL}/auth/signin`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({
      email,
      password,
    }),
  })

  if (!response.ok) {
    const error = await response.json()
    throw new Error(error.error || 'Failed to sign in')
  }

  const data = await response.json()
  storeAuthData(data.access_token, data.refresh_token, data.user)
  return data
}

// Refresh access token
export async function refreshAccessToken() {
  const refreshToken = getRefreshToken()
  if (!refreshToken) {
    throw new Error('No refresh token available')
  }

  const response = await fetch(`${API_URL}/auth/refresh`, {
    method: 'POST',
    credentials: 'include',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({
      refresh_token: refreshToken || '',
    }),
  })

  if (!response.ok) {
    // Refresh token expired or invalid - clear auth data
    clearAuthData()
    throw new Error('Session expired. Please sign in again.')
  }

  const data = await response.json()
  // Update both tokens — backend rotates refresh token on each use
  localStorage.setItem(ACCESS_TOKEN_KEY, data.access_token)
  if (data.refresh_token) {
    localStorage.setItem(REFRESH_TOKEN_KEY, data.refresh_token)
  }
  if (data.user) {
    localStorage.setItem(USER_KEY, JSON.stringify(data.user))
  }
  return data
}

// Get current user (from backend)
export async function getCurrentUser() {
  const accessToken = getAccessToken()
  if (!accessToken) {
    return null
  }

  try {
    const response = await fetch(`${API_URL}/auth/me`, {
      method: 'GET',
      headers: {
        'Authorization': `Bearer ${accessToken}`,
        'Content-Type': 'application/json',
      },
    })

    if (!response.ok) {
      if (response.status === 401) {
        // Token expired, try to refresh
        try {
          await refreshAccessToken()
          // Retry with new token
          const retryResponse = await fetch(`${API_URL}/auth/me`, {
            method: 'GET',
            headers: {
              'Authorization': `Bearer ${getAccessToken()}`,
              'Content-Type': 'application/json',
            },
          })
          if (retryResponse.ok) {
            const user = await retryResponse.json()
            localStorage.setItem(USER_KEY, JSON.stringify(user))
            return user
          }
        } catch {
          clearAuthData()
          return null
        }
      }
      return null
    }

    const user = await response.json()
    localStorage.setItem(USER_KEY, JSON.stringify(user))
    return user
  } catch (error) {
    console.error('Error fetching current user:', error)
    return null
  }
}

// Logout
export async function logout() {
  const refreshToken = getRefreshToken()

  // Try to revoke refresh token on backend
  if (refreshToken) {
    try {
      await fetch(`${API_URL}/auth/logout`, {
        method: 'POST',
        credentials: 'include',
        headers: {
          'Content-Type': 'application/json',
        },
        body: JSON.stringify({
          refresh_token: refreshToken,
        }),
      })
    } catch (error) {
      console.error('Error during logout:', error)
      // Continue with local logout even if backend call fails
    }
  }

  clearAuthData()

  // Desktop app: also clear tokens from electron-store
  if (window.electronAPI?.isDesktop) {
    try {
      await window.electronAPI.logout()
    } catch (error) {
      console.error('Error during desktop logout:', error)
    }
  }
}

// Check if user is authenticated
export function isAuthenticated() {
  return !!getAccessToken()
}

// Get auth headers for API requests
export function getAuthHeaders() {
  const accessToken = getAccessToken()
  if (!accessToken) {
    throw new Error('No access token available')
  }
  return {
    'Authorization': `Bearer ${accessToken}`,
    'Content-Type': 'application/json',
  }
}

// Desktop app fallback: restore tokens from Electron main-process store
// if renderer localStorage does not have them yet.
export async function restoreDesktopAuthFromNative() {
  if (typeof window === 'undefined') {
    return false
  }

  const hasLocalTokens = !!(getAccessToken() && getRefreshToken())
  if (hasLocalTokens) {
    return true
  }

  const isDesktop = Boolean(window.electronAPI?.isDesktop)
  const canReadNativeTokens = typeof window.electronAPI?.getTokens === 'function'
  if (!isDesktop || !canReadNativeTokens) {
    return false
  }

  try {
    const tokens = await window.electronAPI.getTokens()
    if (tokens?.accessToken && tokens?.refreshToken) {
      localStorage.setItem(ACCESS_TOKEN_KEY, tokens.accessToken)
      localStorage.setItem(REFRESH_TOKEN_KEY, tokens.refreshToken)
      // Keep legacy keys in sync for backward compatibility.
      localStorage.setItem('access_token', tokens.accessToken)
      localStorage.setItem('refresh_token', tokens.refreshToken)
      return true
    }
  } catch (error) {
    console.error('Failed to restore desktop auth from native store:', error)
  }

  return false
}
