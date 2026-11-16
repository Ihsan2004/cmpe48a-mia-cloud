import { useState, useEffect } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router-dom'
import { Lock, CheckCircle, AlertCircle } from 'lucide-react'
import { getFriendlyErrorMessage } from '../utils/errorParser'

const API_URL = import.meta.env.VITE_API_URL

export default function ResetPassword() {
  const navigate = useNavigate()
  const [searchParams] = useSearchParams()
  const [password, setPassword] = useState('')
  const [confirmPassword, setConfirmPassword] = useState('')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [success, setSuccess] = useState(false)
  const [validToken, setValidToken] = useState(false)
  const [checkingToken, setCheckingToken] = useState(true)

  useEffect(() => {
    const token = searchParams.get('token')

    if (token) {
      setValidToken(true)
    } else {
      setError('Geçersiz veya eksik reset token. Lütfen şifre sıfırlama işlemini tekrar başlatın.')
    }
    setCheckingToken(false)
  }, [searchParams])

  const handleResetPassword = async (e) => {
    e.preventDefault()

    if (password !== confirmPassword) {
      setError('Şifreler eşleşmiyor')
      return
    }

    if (password.length < 8) {
      setError('Şifre en az 8 karakter olmalıdır')
      return
    }

    setLoading(true)
    setError('')

    try {
      const token = searchParams.get('token')
      if (!token) {
        throw new Error('Reset token is required')
      }

      const response = await fetch(`${API_URL}/auth/reset-password`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
        },
        body: JSON.stringify({
          token,
          new_password: password,
        }),
      })

      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to reset password')
      }

      setSuccess(true)

      setTimeout(() => {
        navigate('/login')
      }, 3000)
    } catch (err) {
      setError(getFriendlyErrorMessage(err))
    } finally {
      setLoading(false)
    }
  }

  if (checkingToken) {
    return (
      <div className="min-h-screen flex items-center justify-center bg-[var(--canvas-bone)]">
        <div className="flex items-center gap-3 text-text-secondary">
          <div className="w-5 h-5 border-2 border-[var(--canvas-muted)] border-t-[var(--accent-ochre-ink)] rounded-full animate-spin" />
          <span className="font-sans text-meta">Doğrulanıyor...</span>
        </div>
      </div>
    )
  }

  return (
    <div className="min-h-screen flex items-center justify-center px-6 py-16 md:px-10 bg-[var(--canvas-bone)]">
      <div className="w-full max-w-md">
        {/* Wordmark */}
        <div className="text-center mb-12">
          <span className="font-serif font-medium tracking-tight text-2xl leading-none text-[var(--ink-charcoal)]">
            syncrun
          </span>
        </div>

        {success ? (
          <div className="text-center">
            <div className="w-14 h-14 flex items-center justify-center mx-auto mb-6 border border-[var(--canvas-muted)]">
              <CheckCircle className="w-6 h-6 text-[var(--accent-ochre-ink)]" strokeWidth={1.5} />
            </div>

            <h2 className="font-serif text-h2 text-[var(--ink-charcoal)] mb-4">Şifre Değiştirildi</h2>
            <p className="text-text-secondary text-body font-sans leading-[1.55] mb-8">
              Şifreniz başarıyla güncellendi. Giriş sayfasına yönlendiriliyorsunuz...
            </p>

            <Link to="/login" className="btn-primary inline-flex">
              Giriş Yap
            </Link>
          </div>
        ) : !validToken ? (
          <div className="text-center">
            <div className="w-14 h-14 flex items-center justify-center mx-auto mb-6 border border-[var(--canvas-muted)]">
              <AlertCircle className="w-6 h-6 text-danger" strokeWidth={1.5} />
            </div>

            <h2 className="font-serif text-h2 text-[var(--ink-charcoal)] mb-4">Geçersiz Link</h2>
            <p className="text-text-secondary text-body font-sans leading-[1.55] mb-8">
              {error}
            </p>

            <Link to="/forgot-password" className="btn-primary inline-flex">
              Tekrar Dene
            </Link>
          </div>
        ) : (
          <>
            <span className="eyebrow">Yeni şifre</span>
            <h2 className="mt-4 font-serif text-h1 text-[var(--ink-charcoal)] mb-3">Yeni Şifre Belirle</h2>
            <p className="text-text-secondary text-body font-sans leading-[1.55] mb-8">
              Hesabınız için yeni bir şifre oluşturun.
            </p>

            {error && (
              <div className="mb-6 p-4 border border-danger/40 text-danger text-meta font-sans">
                {error}
              </div>
            )}

            <form onSubmit={handleResetPassword} className="space-y-5">
              <div>
                <label className="block text-micro uppercase tracking-[0.14em] text-[var(--accent-ochre-ink)] mb-2">
                  Yeni Şifre
                </label>
                <div className="relative">
                  <Lock className="absolute left-4 top-1/2 -translate-y-1/2 text-text-secondary w-4 h-4" strokeWidth={1.5} />
                  <input
                    type="password"
                    value={password}
                    onChange={(e) => setPassword(e.target.value)}
                    className="input-field w-full pl-11"
                    placeholder="••••••••"
                    required
                    minLength={6}
                  />
                </div>
              </div>

              <div>
                <label className="block text-micro uppercase tracking-[0.14em] text-[var(--accent-ochre-ink)] mb-2">
                  Şifre Tekrar
                </label>
                <div className="relative">
                  <Lock className="absolute left-4 top-1/2 -translate-y-1/2 text-text-secondary w-4 h-4" strokeWidth={1.5} />
                  <input
                    type="password"
                    value={confirmPassword}
                    onChange={(e) => setConfirmPassword(e.target.value)}
                    className="input-field w-full pl-11"
                    placeholder="••••••••"
                    required
                    minLength={6}
                  />
                </div>
              </div>

              <button
                type="submit"
                disabled={loading}
                className="btn-primary w-full inline-flex items-center justify-center gap-2 mt-2"
              >
                {loading ? (
                  <>
                    <div className="w-4 h-4 border-2 border-[var(--canvas-bone)] border-t-transparent rounded-full animate-spin" />
                    Kaydediliyor...
                  </>
                ) : (
                  'Şifreyi Değiştir'
                )}
              </button>
            </form>
          </>
        )}
      </div>
    </div>
  )
}
