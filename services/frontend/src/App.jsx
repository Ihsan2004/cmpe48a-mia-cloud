import { BrowserRouter as Router, Routes, Route, Navigate } from 'react-router-dom'
import { useState, useEffect, lazy, Suspense } from 'react'
import { useTranslation } from 'react-i18next'
import { Toaster } from 'sonner'
import { getCurrentUser, logout as authLogout, isAuthenticated, restoreDesktopAuthFromNative } from './services/auth'
import ProtectedRoute from './components/ProtectedRoute'
import ErrorBoundary from './components/ErrorBoundary'
import Sidebar from './components/Sidebar'
import OnboardingGuard from './components/OnboardingGuard'
import { UserContext } from './contexts/UserContext'
import QuotaBanner from './components/QuotaBanner'
import { QuotaProvider } from './context/QuotaContext'

const Login = lazy(() => import('./pages/Login'))
const Signup = lazy(() => import('./pages/Signup'))
const Dashboard = lazy(() => import('./pages/Dashboard'))
const Profile = lazy(() => import('./pages/Profile'))
const PastMeetings = lazy(() => import('./pages/PastMeetings'))
const MeetingDetails = lazy(() => import('./pages/MeetingDetails'))
const ConnectCalendar = lazy(() => import('./pages/ConnectCalendar'))
const ForgotPassword = lazy(() => import('./pages/ForgotPassword'))
const ResetPassword = lazy(() => import('./pages/ResetPassword'))
const PrivacyPolicy = lazy(() => import('./pages/PrivacyPolicy'))
const TermsOfService = lazy(() => import('./pages/TermsOfService'))
const KvkkAydinlatma = lazy(() => import('./pages/KvkkAydinlatma'))
const PilotSozlesmesi = lazy(() => import('./pages/PilotSozlesmesi'))
const ConsentsPending = lazy(() => import('./pages/ConsentsPending'))
const OAuthCallback = lazy(() => import('./pages/OAuthCallback'))
const DecisionReview = lazy(() => import('./pages/DecisionReview'))
const MeetingFlow = lazy(() => import('./pages/Flows/MeetingFlow'))
const SettingsNotifications = lazy(() => import('./pages/SettingsNotifications'))
const OnboardingTeams = lazy(() => import('./pages/OnboardingTeams'))
const Teams = lazy(() => import('./pages/Teams'))
const SettingsUsage = lazy(() => import('./pages/SettingsUsage'))
const ChainSuggestions = lazy(() => import('./pages/ChainSuggestions'))

const API_URL = import.meta.env.VITE_API_URL
if (!API_URL) {
  console.error('VITE_API_URL is not set in environment variables')
}

function App() {
  const { t, i18n } = useTranslation()
  const [user, setUser] = useState(null)
  const [loading, setLoading] = useState(true)
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false)

  useEffect(() => {
    if (user?.interface_language) {
      i18n.changeLanguage(user.interface_language)
      localStorage.setItem('interface_language', user.interface_language)
    }
  }, [user?.interface_language, i18n])

  useEffect(() => {
    const initAuth = async () => {
      await restoreDesktopAuthFromNative()

      if (!isAuthenticated()) {
        setUser(null)
        setLoading(false)
        return
      }

      try {
        const currentUser = await getCurrentUser()
        setUser(currentUser)
      } catch (error) {
        setUser(null)
      } finally {
        setLoading(false)
      }
    }

    initAuth()
  }, [])

  const handleLogout = async () => {
    await authLogout()
    setUser(null)
    window.location.href = '/login'
  }

  if (loading) {
    return (
      <div className="min-h-screen bg-background flex flex-col items-center justify-center gap-4">
        <div className="w-12 h-12 border-2 border-[var(--canvas-muted)] border-t-[var(--accent-ochre-ink)] rounded-full animate-spin" />
        <div className="text-text-secondary text-sm font-sans">{t('common.loading')}</div>
      </div>
    )
  }

  const routeFallback = (
    <div className="min-h-screen bg-background flex flex-col items-center justify-center gap-4">
      <div className="w-12 h-12 border-2 border-[var(--canvas-muted)] border-t-[var(--accent-ochre-ink)] rounded-full animate-spin" />
      <div className="text-text-secondary text-sm font-sans">{t('common.loading')}</div>
    </div>
  )

  return (
    <Router>
      <Toaster
        position="top-right"
        toastOptions={{
          duration: 4000,
          style: {
            background: 'var(--canvas-bone)',
            border: '1px solid var(--canvas-muted)',
            color: 'var(--ink-charcoal)',
            fontFamily: 'var(--font-sans)',
            borderRadius: '0',
          },
          error: { duration: 6000 },
          success: { duration: 3000 },
        }}
      />
      <ErrorBoundary>
      <Suspense fallback={routeFallback}>
        <Routes>
          <Route
            path="/login"
            element={user ? <Navigate to="/dashboard" /> : <Login onLogin={setUser} />}
          />
          <Route
            path="/signup"
            element={user ? <Navigate to="/dashboard" /> : <Signup onLogin={setUser} />}
          />
          <Route
            path="/forgot-password"
            element={user ? <Navigate to="/dashboard" /> : <ForgotPassword />}
          />
          <Route
            path="/reset-password"
            element={<ResetPassword />}
          />
          <Route
            path="/privacy-policy"
            element={<PrivacyPolicy />}
          />
          <Route
            path="/terms-of-service"
            element={<TermsOfService />}
          />
          <Route
            path="/kvkk"
            element={<KvkkAydinlatma />}
          />
          <Route
            path="/pilot-sozlesmesi"
            element={<PilotSozlesmesi />}
          />
          <Route
            path="/auth/callback"
            element={<OAuthCallback onLogin={setUser} />}
          />
          {/* Consent gate — protected (auth required) but EXEMPT from
              consent gating to avoid an infinite redirect loop. */}
          <Route
            path="/consents-pending"
            element={
              <ProtectedRoute requiresConsents={false}>
                <ConsentsPending />
              </ProtectedRoute>
            }
          />
          <Route
            path="/connect-calendar"
            element={
              <ProtectedRoute>
                <ConnectCalendar />
              </ProtectedRoute>
            }
          />
          {/* Phase 7 (D-27 + D-28): full-screen onboarding peer of
              /connect-calendar — sits OUTSIDE the sidebar layout. */}
          <Route
            path="/onboarding/teams"
            element={
              <ProtectedRoute>
                <UserContext.Provider value={user}>
                  <OnboardingGuard mode="onboarding">
                    <OnboardingTeams />
                  </OnboardingGuard>
                </UserContext.Provider>
              </ProtectedRoute>
            }
          />
          <Route
            path="/*"
            element={
              <ProtectedRoute>
                <UserContext.Provider value={user}>
                <QuotaProvider authenticated={!!user}>
                  <div className="flex h-screen overflow-hidden bg-background">
                    <Sidebar
                      user={user}
                      onLogout={handleLogout}
                      collapsed={sidebarCollapsed}
                      onToggle={() => setSidebarCollapsed((v) => !v)}
                    />

                    <div className={`flex-1 flex flex-col min-w-0 transition-all duration-200 overflow-y-auto ${sidebarCollapsed ? 'pl-16' : ''}`}>
                      <QuotaBanner />
                      <Routes>
                        <Route path="/dashboard" element={<Dashboard />} />
                        <Route path="/profile" element={<Profile onUserUpdate={setUser} />} />
                        <Route path="/meetings" element={<PastMeetings />} />
                        <Route path="/meetings/:id" element={<MeetingDetails />} />
                        <Route path="/meetings/:id/review" element={<DecisionReview />} />
                        <Route path="/flows" element={<MeetingFlow />} />
                        <Route path="/settings/notifications" element={<SettingsNotifications />} />
                        <Route path="/settings/usage" element={<SettingsUsage />} />
                        <Route path="/chain-suggestions" element={<ChainSuggestions />} />
                        {/* Phase 7 (D-28): /teams is reverse-guarded — sends
                            pending users back into /onboarding/teams. */}
                        <Route
                          path="/teams"
                          element={
                            <OnboardingGuard mode="teams">
                              <Teams />
                            </OnboardingGuard>
                          }
                        />
                        <Route path="/" element={<Navigate to="/dashboard" />} />
                      </Routes>
                    </div>
                  </div>
                </QuotaProvider>
                </UserContext.Provider>
              </ProtectedRoute>
            }
          />
        </Routes>
      </Suspense>
      </ErrorBoundary>
    </Router>
  )
}

export default App
