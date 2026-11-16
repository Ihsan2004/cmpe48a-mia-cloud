import { useState, useEffect } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { getStoredUser } from '../services/auth'
import { checkCalendarConnection, getCalendarConnectURL, saveGoogleToken, getProfile } from '../services/api'
import { Calendar, CheckCircle, Sparkles, ArrowRight, Loader, AlertCircle } from 'lucide-react'
import { getFriendlyErrorMessage } from '../utils/errorParser'

export default function ConnectCalendar() {
  const navigate = useNavigate()
  const [searchParams] = useSearchParams()
  const [loading, setLoading] = useState(true)
  const [connecting, setConnecting] = useState(false)
  const [error, setError] = useState('')
  const [isConnected, setIsConnected] = useState(false)
  const [step, setStep] = useState(1) // 1: Welcome, 2: Connect, 3: Success

  useEffect(() => {
    checkConnection()

    const token = searchParams.get('token')
    const refreshToken = searchParams.get('refresh_token')

    if (refreshToken) {
      handleOAuthCallback(refreshToken)
    }
  }, [searchParams])

  const checkConnection = async () => {
    try {
      const result = await checkCalendarConnection()
      setIsConnected(result.is_calendar_connected)

      if (result.is_calendar_connected) {
        // D-27 + RESEARCH Pitfall 6: auto-redirect into the Phase 7
        // onboarding flow ONLY when the user has never finalized teams
        // onboarding. A user revisiting /connect-calendar after they've
        // already saved/skipped must NOT be re-routed.
        try {
          const profile = await getProfile()
          if (profile?.teams_onboarding_status === 'pending') {
            navigate('/onboarding/teams', { replace: true })
            return
          }
        } catch {
          // Profile fetch failed — treat as already-onboarded (safer than
          // forcing onboarding on a user who may already be finalized).
          // Fall through to the step-3 success screen.
        }
        setStep(3)
      }
    } catch (err) {
      console.error('Failed to check calendar connection:', err)
    } finally {
      setLoading(false)
    }
  }

  const handleOAuthCallback = async (refreshToken) => {
    setConnecting(true)
    setError('')

    try {
      const user = getStoredUser()
      await saveGoogleToken(refreshToken, user?.email)
      window.history.replaceState({}, document.title, '/connect-calendar')
      setIsConnected(true)
      // D-27 (RESEARCH R2 mitigation): handleOAuthCallback runs in parallel
      // with checkConnection on the same mount, but its success path sets
      // step=3 directly. Mirror the pending-redirect gate here so the user
      // is funneled into /onboarding/teams immediately after OAuth, not
      // after they reload the page.
      try {
        const profile = await getProfile()
        if (profile?.teams_onboarding_status === 'pending') {
          navigate('/onboarding/teams', { replace: true })
          return
        }
      } catch {
        // Profile fetch failed — fall through to step-3 (safer fallback).
      }
      setStep(3)
    } catch (err) {
      setError(getFriendlyErrorMessage(err))
    } finally {
      setConnecting(false)
    }
  }

  const handleConnectCalendar = async () => {
    setConnecting(true)
    setError('')

    try {
      const data = await getCalendarConnectURL()

      localStorage.removeItem('oauth_state')
      sessionStorage.removeItem('oauth_state')

      if (data.state) {
        localStorage.setItem('oauth_state', data.state)
      }

      window.location.href = data.auth_url
    } catch (err) {
      setError(getFriendlyErrorMessage(err))
      setConnecting(false)
    }
  }

  const handleSkip = () => {
    if (window.confirm('Google Takvim bağlantısı olmadan syncrun toplantılarınızı otomatik olarak algılayamaz. Yine de atlamak istiyor musunuz?')) {
      navigate('/dashboard')
    }
  }

  const handleContinue = () => {
    navigate('/dashboard')
  }

  if (loading) {
    return (
      <div className="min-h-screen flex items-center justify-center bg-[var(--canvas-bone)]">
        <div className="flex items-center gap-3 text-text-secondary font-sans text-meta">
          <Loader className="w-5 h-5 animate-spin" strokeWidth={1.5} />
          <span>Yükleniyor...</span>
        </div>
      </div>
    )
  }

  return (
    <div className="min-h-screen flex items-center justify-center px-6 py-16 md:px-10 bg-[var(--canvas-bone)]">
      <div className="w-full max-w-lg">
        {/* Wordmark */}
        <div className="text-center mb-10">
          <span className="font-serif font-medium tracking-tight text-2xl leading-none text-[var(--ink-charcoal)]">
            syncrun
          </span>
        </div>

        {/* Progress */}
        <div className="flex items-center justify-center gap-2 mb-10">
          {[1, 2, 3].map((s) => (
            <div
              key={s}
              className={`h-[2px] transition-all duration-200 ${
                s <= step ? 'w-10 bg-[var(--ink-charcoal)]' : 'w-6 bg-[var(--canvas-muted)]'
              }`}
            />
          ))}
        </div>

        {/* Card */}
        <div className="border border-[var(--canvas-muted)] p-8 md:p-10">
          {/* Step 1: Welcome */}
          {step === 1 && (
            <div className="text-center">
              <div className="w-14 h-14 flex items-center justify-center mx-auto mb-6 border border-[var(--canvas-muted)]">
                <Sparkles className="w-6 h-6 text-[var(--accent-ochre-ink)]" strokeWidth={1.5} />
              </div>

              <span className="eyebrow">Kurulum</span>
              <h1 className="mt-4 font-serif text-h2 text-[var(--ink-charcoal)] mb-3">
                Hoş geldiniz
              </h1>
              <p className="text-text-secondary text-body font-sans leading-[1.55] mb-8">
                Hesabınız oluşturuldu. syncrun'ın toplantılarınızı otomatik olarak algılayabilmesi için
                Google Takvim'inizi bağlamanız gerekiyor.
              </p>

              <button
                onClick={() => setStep(2)}
                className="btn-primary w-full inline-flex items-center justify-center gap-2"
              >
                Devam Et
                <ArrowRight className="w-4 h-4" strokeWidth={1.5} />
              </button>
            </div>
          )}

          {/* Step 2: Connect Calendar */}
          {step === 2 && (
            <div>
              <div className="w-14 h-14 flex items-center justify-center mx-auto mb-6 border border-[var(--canvas-muted)]">
                <Calendar className="w-6 h-6 text-[var(--accent-ochre-ink)]" strokeWidth={1.5} />
              </div>

              <span className="eyebrow block text-center">Erişim</span>
              <h2 className="mt-4 font-serif text-h2 text-[var(--ink-charcoal)] mb-3 text-center">
                Google Takvim'i Bağlayın
              </h2>
              <p className="text-text-secondary text-body font-sans leading-[1.55] mb-8 text-center">
                syncrun, toplantılarınızı otomatik olarak algılayıp kaydetmek için Google Takvim'e erişim izni istiyor.
              </p>

              <div className="border-t border-[var(--canvas-muted)] pt-6 mb-6">
                <p className="eyebrow mb-4">İzinler</p>
                <ul className="space-y-3 text-meta font-sans">
                  <li className="flex items-center gap-3 text-text-secondary">
                    <CheckCircle className="w-4 h-4 text-[var(--accent-ochre-ink)] flex-shrink-0" strokeWidth={1.5} />
                    Takvim etkinliklerini görüntüleme
                  </li>
                  <li className="flex items-center gap-3 text-text-secondary">
                    <CheckCircle className="w-4 h-4 text-[var(--accent-ochre-ink)] flex-shrink-0" strokeWidth={1.5} />
                    Toplantı oluşturma ve düzenleme
                  </li>
                  <li className="flex items-center gap-3 text-text-secondary">
                    <CheckCircle className="w-4 h-4 text-[var(--accent-ochre-ink)] flex-shrink-0" strokeWidth={1.5} />
                    E-posta adresinizi görüntüleme
                  </li>
                </ul>
              </div>

              {error && (
                <div className="mb-6 p-4 border border-danger/40 text-danger text-meta font-sans flex items-center gap-2">
                  <AlertCircle className="w-4 h-4 flex-shrink-0" strokeWidth={1.5} />
                  {error}
                </div>
              )}

              <button
                onClick={handleConnectCalendar}
                disabled={connecting}
                className="btn-primary w-full inline-flex items-center justify-center gap-3 mb-3"
              >
                {connecting ? (
                  <>
                    <Loader className="w-4 h-4 animate-spin" strokeWidth={1.5} />
                    Bağlanıyor...
                  </>
                ) : (
                  <>
                    <svg className="w-4 h-4" viewBox="0 0 24 24">
                      <path fill="currentColor" d="M22.56 12.25c0-.78-.07-1.53-.2-2.25H12v4.26h5.92c-.26 1.37-1.04 2.53-2.21 3.31v2.77h3.57c2.08-1.92 3.28-4.74 3.28-8.09z" />
                      <path fill="currentColor" d="M12 23c2.97 0 5.46-.98 7.28-2.66l-3.57-2.77c-.98.66-2.23 1.06-3.71 1.06-2.86 0-5.29-1.93-6.16-4.53H2.18v2.84C3.99 20.53 7.7 23 12 23z" />
                      <path fill="currentColor" d="M5.84 14.09c-.22-.66-.35-1.36-.35-2.09s.13-1.43.35-2.09V7.07H2.18C1.43 8.55 1 10.22 1 12s.43 3.45 1.18 4.93l2.85-2.22.81-.62z" />
                      <path fill="currentColor" d="M12 5.38c1.62 0 3.06.56 4.21 1.64l3.15-3.15C17.45 2.09 14.97 1 12 1 7.7 1 3.99 3.47 2.18 7.07l3.66 2.84c.87-2.6 3.3-4.53 6.16-4.53z" />
                    </svg>
                    Google Takvimi Bağla
                  </>
                )}
              </button>

              <button
                onClick={handleSkip}
                disabled={connecting}
                className="btn-ghost w-full"
              >
                Şimdilik Atla
              </button>
            </div>
          )}

          {/* Step 3: Success */}
          {step === 3 && (
            <div className="text-center">
              <div className="w-14 h-14 flex items-center justify-center mx-auto mb-6 border border-[var(--canvas-muted)]">
                <CheckCircle className="w-6 h-6 text-[var(--accent-ochre-ink)]" strokeWidth={1.5} />
              </div>

              <span className="eyebrow">Tamamlandı</span>
              <h2 className="mt-4 font-serif text-h2 text-[var(--ink-charcoal)] mb-3">
                Bağlantı kuruldu
              </h2>
              <p className="text-text-secondary text-body font-sans leading-[1.55] mb-8">
                Google Takvim başarıyla bağlandı. syncrun artık toplantılarınızı otomatik olarak
                algılayabilir ve kayıt altına alabilir.
              </p>

              <div className="border-t border-[var(--canvas-muted)] pt-6 mb-8 text-left">
                <p className="eyebrow mb-4">Artık yapabilecekleriniz</p>
                <ul className="space-y-3 text-meta font-sans">
                  <li className="flex items-center gap-3 text-text-secondary">
                    <span className="w-1 h-1 rounded-full bg-[var(--accent-ochre)]" />
                    Otomatik toplantı kaydı
                  </li>
                  <li className="flex items-center gap-3 text-text-secondary">
                    <span className="w-1 h-1 rounded-full bg-[var(--accent-ochre)]" />
                    Gerçek zamanlı transkript
                  </li>
                  <li className="flex items-center gap-3 text-text-secondary">
                    <span className="w-1 h-1 rounded-full bg-[var(--accent-ochre)]" />
                    Yapay zeka destekli özetler
                  </li>
                  <li className="flex items-center gap-3 text-text-secondary">
                    <span className="w-1 h-1 rounded-full bg-[var(--accent-ochre)]" />
                    Akıllı araştırma önerileri
                  </li>
                </ul>
              </div>

              <button
                onClick={handleContinue}
                className="btn-primary w-full inline-flex items-center justify-center gap-2"
              >
                Dashboard'a Git
                <ArrowRight className="w-4 h-4" strokeWidth={1.5} />
              </button>
            </div>
          )}
        </div>

        {/* Footer */}
        <p className="mt-8 text-center text-micro text-text-tertiary font-sans">
          İstediğiniz zaman Ayarlar'dan takvim bağlantınızı yönetebilirsiniz.
        </p>
      </div>
    </div>
  )
}
