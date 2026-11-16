import { useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { signIn } from '../services/auth'
import { Mail, Lock, ArrowRight, AlertTriangle, Eye, EyeOff } from 'lucide-react'
import { getFriendlyErrorMessage } from '../utils/errorParser'

const API_URL = import.meta.env.VITE_API_URL
const PASSWORD_AUTH_DISABLED = import.meta.env.VITE_DISABLE_PASSWORD_AUTH === 'true'

export default function Login({ onLogin }) {
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [showPassword, setShowPassword] = useState(false)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const navigate = useNavigate()

  const handleEmailLogin = async (e) => {
    e.preventDefault()
    setLoading(true)
    setError('')

    try {
      const data = await signIn(email, password)
      if (onLogin && data.user) {
        onLogin(data.user)
      }
      navigate('/dashboard')
    } catch (err) {
      setError(getFriendlyErrorMessage(err))
    } finally {
      setLoading(false)
    }
  }

  const handleGoogleLogin = async () => {
    setLoading(true)
    setError('')

    try {
      const isDesktopApp = Boolean(
        window.electronAPI?.isElectron
        || window.location.protocol === 'file:'
        || navigator.userAgent.toLowerCase().includes('electron'),
      )
      if (isDesktopApp && window.electronAPI?.googleLogin) {
        const result = await window.electronAPI.googleLogin('login')
        setLoading(false)
        if (!result?.success) {
          setError('Google ile giriş yapılamadı. Lütfen tekrar deneyin.')
        }
        return
      }

      const response = await fetch(`${API_URL}/auth/google?mode=login`, {
        credentials: 'include',
      })
      if (!response.ok) {
        throw new Error('Failed to get Google OAuth URL')
      }

      const data = await response.json()

      localStorage.removeItem('oauth_state')
      sessionStorage.removeItem('oauth_state')

      localStorage.setItem('oauth_state', data.state)

      setLoading(false)
      window.location.href = data.auth_url
    } catch (err) {
      setError(getFriendlyErrorMessage(err))
      setLoading(false)
    }
  }

  return (
    <div className="min-h-screen w-full bg-[var(--canvas-bone)] flex items-center justify-center px-6 py-16 md:px-10">
      <div className="w-full max-w-md">
        {/* Wordmark */}
        <div className="mb-12 text-center">
          <span className="font-serif font-medium tracking-tight text-2xl leading-none text-[var(--ink-charcoal)]">
            syncrun
          </span>
        </div>

        {/* Welcome Text */}
        <div className="mb-10 fade-in-up">
          <span className="eyebrow">Giriş</span>
          <h1 className="mt-4 font-serif text-h1 text-[var(--ink-charcoal)]">
            Hoşgeldiniz
          </h1>
          <p className="mt-3 text-text-secondary text-body font-sans leading-[1.55]">
            Hesabınıza giriş yapın.
          </p>
        </div>

        {/* Test Version Notice */}
        <div className="mb-8 p-6 border border-[var(--canvas-muted)] flex gap-4 fade-in-up">
          <AlertTriangle className="w-5 h-5 text-[var(--accent-ochre-ink)] shrink-0 mt-0.5" strokeWidth={1.5} />
          <div>
            <p className="font-serif text-[17px] text-[var(--ink-charcoal)] mb-1">Beta Sürümü</p>
            <p className="text-meta text-text-secondary font-sans leading-[1.55]">
              syncrun şu anda kapalı beta aşamasındadır. Erişim sadece davetli kullanıcılarla sınırlıdır.
            </p>
          </div>
        </div>

        {/* Login Form Container */}
        <div className="fade-in-up">
          {error && (
            <div className="mb-6 p-4 border border-danger/40 text-danger text-meta font-sans">
              {error}
            </div>
          )}

          {!PASSWORD_AUTH_DISABLED && (
          <form onSubmit={handleEmailLogin} className="space-y-5">
            <div>
              <label className="block text-micro uppercase tracking-[0.14em] text-[var(--accent-ochre-ink)] mb-2">
                E-posta
              </label>
              <div className="relative group">
                <div className="absolute inset-y-0 left-0 pl-4 flex items-center pointer-events-none">
                  <Mail className="h-4 w-4 text-text-secondary" strokeWidth={1.5} />
                </div>
                <input
                  type="email"
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                  className="input-field w-full pl-11"
                  placeholder="example@email.com"
                  required
                />
              </div>
            </div>

            <div>
              <div className="flex items-center justify-between mb-2">
                <label className="text-micro uppercase tracking-[0.14em] text-[var(--accent-ochre-ink)]">
                  Şifre
                </label>
                <Link
                  to="/forgot-password"
                  className="text-micro text-text-secondary underline underline-offset-[6px] decoration-[var(--accent-ochre-ink)] hover:decoration-[var(--ink-charcoal)] transition-colors"
                >
                  Şifremi Unuttum
                </Link>
              </div>
              <div className="relative group">
                <div className="absolute inset-y-0 left-0 pl-4 flex items-center pointer-events-none">
                  <Lock className="h-4 w-4 text-text-secondary" strokeWidth={1.5} />
                </div>
                <input
                  type={showPassword ? 'text' : 'password'}
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  className="input-field w-full pl-11 pr-11"
                  placeholder="••••••••"
                  required
                />
                <button
                  type="button"
                  onClick={() => setShowPassword(!showPassword)}
                  className="absolute right-0 top-0 h-full px-4 flex items-center text-text-secondary hover:text-[var(--ink-charcoal)] transition-colors"
                >
                  {showPassword ? (
                    <EyeOff className="h-4 w-4" strokeWidth={1.5} />
                  ) : (
                    <Eye className="h-4 w-4" strokeWidth={1.5} />
                  )}
                </button>
              </div>
            </div>

            <button
              type="submit"
              disabled={loading}
              className="btn-primary w-full inline-flex items-center justify-center gap-2 mt-4"
            >
              {loading ? (
                <>
                  <div className="w-4 h-4 border-2 border-[var(--canvas-bone)] border-t-transparent rounded-full animate-spin" />
                  Giriş Yapılıyor...
                </>
              ) : (
                <>
                  Giriş Yap
                  <ArrowRight className="w-4 h-4" strokeWidth={1.5} />
                </>
              )}
            </button>
          </form>
          )}

          {!PASSWORD_AUTH_DISABLED && (
          <div className="relative my-8">
            <div className="absolute inset-0 flex items-center">
              <div className="w-full border-t border-[var(--canvas-muted)]"></div>
            </div>
            <div className="relative flex justify-center">
              <span className="px-4 text-micro uppercase tracking-[0.14em] text-text-secondary bg-[var(--canvas-bone)]">
                veya
              </span>
            </div>
          </div>
          )}

          {/* Google Login */}
          <button
            onClick={handleGoogleLogin}
            disabled={loading}
            className="w-full inline-flex items-center justify-center gap-3 h-12 border border-[var(--canvas-muted)] hover:border-[var(--ink-secondary)] text-[var(--ink-charcoal)] text-body font-sans font-medium rounded-full transition-colors duration-150 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent-ochre-ink)] focus-visible:ring-offset-2 focus-visible:ring-offset-[var(--canvas-bone)] disabled:opacity-40 disabled:cursor-not-allowed"
          >
            <svg className="w-4 h-4" viewBox="0 0 24 24">
              <path
                fill="currentColor"
                d="M22.56 12.25c0-.78-.07-1.53-.2-2.25H12v4.26h5.92c-.26 1.37-1.04 2.53-2.21 3.31v2.77h3.57c2.08-1.92 3.28-4.74 3.28-8.09z"
                className="fill-[#4285F4]"
              />
              <path
                fill="currentColor"
                d="M12 23c2.97 0 5.46-.98 7.28-2.66l-3.57-2.77c-.98.66-2.23 1.06-3.71 1.06-2.86 0-5.29-1.93-6.16-4.53H2.18v2.84C3.99 20.53 7.7 23 12 23z"
                className="fill-[#34A853]"
              />
              <path
                fill="currentColor"
                d="M5.84 14.09c-.22-.66-.35-1.36-.35-2.09s.13-1.43.35-2.09V7.07H2.18C1.43 8.55 1 10.22 1 12s.43 3.45 1.18 4.93l2.85-2.22.81-.62z"
                className="fill-[#FBBC05]"
              />
              <path
                fill="currentColor"
                d="M12 5.38c1.62 0 3.06.56 4.21 1.64l3.15-3.15C17.45 2.09 14.97 1 12 1 7.7 1 3.99 3.47 2.18 7.07l3.66 2.84c.87-2.6 3.3-4.53 6.16-4.53z"
                className="fill-[#EA4335]"
              />
            </svg>
            <span>Google ile Devam Et</span>
          </button>

          {/* Signup Link */}
          {!PASSWORD_AUTH_DISABLED && (
          <p className="mt-8 text-center text-meta text-text-secondary font-sans">
            Hesabınız yok mu?{' '}
            <Link
              to="/signup"
              className="text-[var(--ink-charcoal)] underline underline-offset-[6px] decoration-[var(--accent-ochre-ink)] hover:decoration-[var(--ink-charcoal)] transition-colors"
            >
              Kayıt Ol
            </Link>
          </p>
          )}
        </div>

        {/* Footer Links */}
        <div className="mt-12 text-center space-x-3 text-micro text-text-tertiary font-sans">
          <Link to="/privacy-policy" className="hover:text-[var(--ink-charcoal)] transition-colors">
            Gizlilik Politikası
          </Link>
          <span>•</span>
          <Link to="/terms-of-service" className="hover:text-[var(--ink-charcoal)] transition-colors">
            Kullanım Şartları
          </Link>
        </div>
      </div>
    </div>
  )
}
