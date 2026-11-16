import { useState } from 'react'
import { Link } from 'react-router-dom'
import { Mail, ArrowLeft, CheckCircle } from 'lucide-react'
import { getFriendlyErrorMessage } from '../utils/errorParser'

const API_URL = import.meta.env.VITE_API_URL

export default function ForgotPassword() {
  const [email, setEmail] = useState('')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [success, setSuccess] = useState(false)

  const handleResetPassword = async (e) => {
    e.preventDefault()
    setLoading(true)
    setError('')

    try {
      const response = await fetch(`${API_URL}/auth/forgot-password`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
        },
        body: JSON.stringify({ email }),
      })

      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to send reset email')
      }

      setSuccess(true)
    } catch (err) {
      setError(getFriendlyErrorMessage(err))
    } finally {
      setLoading(false)
    }
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
          // Success State
          <div className="text-center">
            <div className="w-14 h-14 flex items-center justify-center mx-auto mb-6 border border-[var(--canvas-muted)]">
              <CheckCircle className="w-6 h-6 text-[var(--accent-ochre-ink)]" strokeWidth={1.5} />
            </div>

            <h2 className="font-serif text-h2 text-[var(--ink-charcoal)] mb-4">E-posta Gönderildi</h2>
            <p className="text-text-secondary text-body font-sans leading-[1.55] mb-8 max-w-sm mx-auto">
              Şifre sıfırlama linki <span className="text-[var(--ink-charcoal)]">{email}</span> adresine gönderildi.
              Lütfen e-postanızı kontrol edin.
            </p>

            <Link
              to="/login"
              className="inline-flex items-center gap-2 text-body text-[var(--ink-charcoal)] underline underline-offset-[8px] decoration-[var(--accent-ochre-ink)] hover:decoration-[var(--ink-charcoal)] transition-colors font-sans"
            >
              <ArrowLeft className="w-4 h-4" strokeWidth={1.5} />
              Giriş sayfasına dön
            </Link>
          </div>
        ) : (
          // Form State
          <>
            <Link
              to="/login"
              className="inline-flex items-center gap-2 text-text-secondary hover:text-[var(--ink-charcoal)] text-meta mb-8 transition-colors font-sans"
            >
              <ArrowLeft className="w-4 h-4" strokeWidth={1.5} />
              Geri
            </Link>

            <span className="eyebrow">Hesap kurtarma</span>
            <h2 className="mt-4 font-serif text-h1 text-[var(--ink-charcoal)] mb-3">Şifremi Unuttum</h2>
            <p className="text-text-secondary text-body font-sans leading-[1.55] mb-8">
              E-posta adresinizi girin, size şifre sıfırlama linki gönderelim.
            </p>

            {error && (
              <div className="mb-6 p-4 border border-danger/40 text-danger text-meta font-sans">
                {error}
              </div>
            )}

            <form onSubmit={handleResetPassword} className="space-y-5">
              <div>
                <label className="block text-micro uppercase tracking-[0.14em] text-[var(--accent-ochre-ink)] mb-2">
                  E-posta
                </label>
                <div className="relative">
                  <Mail className="absolute left-4 top-1/2 -translate-y-1/2 text-text-secondary w-4 h-4" strokeWidth={1.5} />
                  <input
                    type="email"
                    value={email}
                    onChange={(e) => setEmail(e.target.value)}
                    className="input-field w-full pl-11"
                    placeholder="ornek@email.com"
                    required
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
                    Gönderiliyor...
                  </>
                ) : (
                  'Sıfırlama Linki Gönder'
                )}
              </button>
            </form>

            <p className="mt-8 text-center text-meta text-text-secondary font-sans">
              Şifrenizi hatırladınız mı?{' '}
              <Link
                to="/login"
                className="text-[var(--ink-charcoal)] underline underline-offset-[6px] decoration-[var(--accent-ochre-ink)] hover:decoration-[var(--ink-charcoal)] transition-colors"
              >
                Giriş Yap
              </Link>
            </p>
          </>
        )}
      </div>
    </div>
  )
}
