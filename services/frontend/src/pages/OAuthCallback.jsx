import { useState, useEffect } from 'react'
import { useSearchParams, useNavigate } from 'react-router-dom'
import { storeAuthData } from '../services/auth'
import { recordPendingConsents } from '../utils/consents'

const API_URL = import.meta.env.VITE_API_URL

// POST any sessionStorage-queued consents (from a Google OAuth signup
// flow) AFTER tokens are stored. On success the queue is cleared. On
// failure the queue is preserved so the consents-pending gate page
// (Faz 1.5c) can retry on next dashboard load. We intentionally do
// not block navigation on failure — the user is already authenticated
// and the gate will catch them.
async function tryRecordPendingConsents(accessToken) {
    if (!API_URL || !accessToken) return
    try {
        await recordPendingConsents(API_URL, accessToken)
    } catch (err) {
        console.error('Pending consents POST failed; gate page will retry', err)
    }
}

export default function OAuthCallback({ onLogin }) {
    const [searchParams] = useSearchParams()
    const [loading, setLoading] = useState(true)
    const [error, setError] = useState('')
    const navigate = useNavigate()

    useEffect(() => {
        // Check if this is a Google OAuth callback (has code and state)
        const code = searchParams.get('code')
        const state = searchParams.get('state')

        if (code && state) {
            // This is a Google OAuth callback - need to send to backend
            // Try localStorage first (more reliable), then sessionStorage as fallback
            let storedState = localStorage.getItem('oauth_state')
            if (!storedState) {
                storedState = sessionStorage.getItem('oauth_state')
            }

            if (!storedState || storedState !== state) {
                // More detailed error message
                const errorMsg = !storedState
                    ? 'State not found. Please try logging in again from the login page.'
                    : `State mismatch. Stored: ${storedState?.substring(0, 20)}..., Received: ${state.substring(0, 20)}...`
                console.error('State validation failed:', errorMsg)
                setError(errorMsg)
                setLoading(false)
                return
            }

            // Send code and state to backend callback endpoint
            if (!API_URL) {
                setError('API URL not configured. Please set VITE_API_URL in environment variables.')
                setLoading(false)
                return
            }
            fetch(`${API_URL}/auth/google/callback?code=${encodeURIComponent(code)}&state=${encodeURIComponent(state)}`, {
                method: 'GET',
                headers: {
                    'X-OAuth-State': state, // Send state in header
                },
                credentials: 'include',
            })
                .then(response => {
                    if (!response.ok) {
                        return response.json().then(data => {
                            throw new Error(data.error || 'Failed to process OAuth callback')
                        })
                    }
                    return response.json()
                })
                .then(async data => {
                    // Backend returns JWT tokens and user data
                    if (data.access_token && data.refresh_token) {
                        // Store auth data (JWT tokens)
                        storeAuthData(data.access_token, data.refresh_token, data.user)

                        // Also store Google token for backward compatibility (if provided)
                        if (data.google_token) {
                            localStorage.setItem('google_access_token', data.google_token)
                        }

                        // Clear state from both localStorage and sessionStorage
                        localStorage.removeItem('oauth_state')
                        sessionStorage.removeItem('oauth_state')

                        // POST any queued signup consents BEFORE navigating
                        // so the dashboard sees a consented user from the
                        // first request. Awaited — we accept the small
                        // additional latency for correctness.
                        await tryRecordPendingConsents(data.access_token)

                        // Update parent state
                        if (onLogin && data.user) {
                            onLogin(data.user)
                        }

                        // Check if this was a calendar connection flow (state starts with "connect_")
                        let decodedState = ''
                        try {
                            decodedState = atob(state)
                        } catch (e) {
                            // Ignore decode errors
                        }

                        if (decodedState.startsWith('connect_')) {
                            // Redirect to connect-calendar
                            navigate('/connect-calendar')
                        } else {
                            // Regular login/signup - redirect to dashboard
                            navigate('/dashboard')
                        }
                    } else {
                        throw new Error('No tokens in response')
                    }
                })
                .catch(err => {
                    setError(err.message)
                    setLoading(false)
                })
        } else {
            // Direct token callback (from backend redirect URL)
            const accessToken = searchParams.get('access_token')
            const refreshToken = searchParams.get('refresh_token')

            if (accessToken && refreshToken) {
                // Get user data from backend
                fetch(`${API_URL}/auth/me`, {
                    method: 'GET',
                    headers: {
                        'Authorization': `Bearer ${accessToken}`,
                        'Content-Type': 'application/json',
                    },
                })
                    .then(userResponse => {
                        if (userResponse.ok) {
                            return userResponse.json().then(user => {
                                storeAuthData(accessToken, refreshToken, user)
                                if (onLogin && user) {
                                    onLogin(user)
                                }
                            })
                        } else {
                            // Still store tokens even if user fetch fails
                            storeAuthData(accessToken, refreshToken, null)
                        }
                    })
                    .catch(err => {
                        console.error('Error fetching user data:', err)
                        // Still store tokens
                        storeAuthData(accessToken, refreshToken, null)
                    })
                    .then(() => tryRecordPendingConsents(accessToken))
                    .finally(() => {
                        // Clear state
                        localStorage.removeItem('oauth_state')
                        sessionStorage.removeItem('oauth_state')

                        // Redirect to dashboard
                        navigate('/dashboard')
                    })
            } else {
                setError('No tokens received from OAuth callback')
                setLoading(false)
            }
        }
    }, [searchParams, navigate, onLogin])

    if (loading) {
        return (
            <div className="min-h-screen bg-background flex items-center justify-center">
                <div className="text-text font-sans">Authenticating...</div>
            </div>
        )
    }

    if (error) {
        return (
            <div className="min-h-screen bg-background flex items-center justify-center">
                <div className="text-danger font-sans">{error}</div>
            </div>
        )
    }

    return null
}
