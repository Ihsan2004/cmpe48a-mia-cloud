import { useEffect, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { AlertTriangle, ArrowRight } from 'lucide-react'
import { getConsentStatus, recordConsents } from '../services/consents'
import { getAccessToken } from '../services/auth'
import {
    buildReacceptConsents,
    loadPendingConsents,
    clearPendingConsents,
    recordPendingConsents,
} from '../utils/consents'

const API_URL = import.meta.env.VITE_API_URL

// Human-readable labels for the consent types shown on the gate. Keep
// in sync with the consent_type CHECK list in
// sql/migrations/020_user_consents.sql.
const CONSENT_LABELS = {
    terms_of_service: 'Kullanım Koşulları',
    privacy_policy: 'Gizlilik Politikası',
    kvkk_aydinlatma: 'KVKK Aydınlatma Metni',
    cross_border_transfer: 'Yurt Dışı Veri Aktarımı Açık Rızası',
    ai_processing: 'Toplantı İçeriklerinin Yapay Zekâ ile İşlenmesi Açık Rızası',
}

const CONSENT_LINKS = {
    terms_of_service: '/terms-of-service',
    privacy_policy: '/privacy-policy',
    kvkk_aydinlatma: '/kvkk',
    cross_border_transfer: '/kvkk',
    ai_processing: '/privacy-policy',
}

export default function ConsentsPending() {
    const [status, setStatus] = useState(null)
    const [loading, setLoading] = useState(true)
    const [submitting, setSubmitting] = useState(false)
    const [error, setError] = useState('')
    const [accepted, setAccepted] = useState(false)
    const navigate = useNavigate()

    useEffect(() => {
        let cancelled = false

        const init = async () => {
            // Step 1: if a previous OAuth signup left pending consents
            // in sessionStorage, try to flush them now before bothering
            // the user — covers the "POST failed during callback" case.
            const pending = loadPendingConsents()
            if (pending && pending.length > 0) {
                try {
                    await recordPendingConsents(API_URL, getAccessToken())
                } catch (err) {
                    // Keep the queue; we'll fall through to the manual
                    // form so the user can complete the flow another way.
                    console.warn('Auto-flush of pending consents failed', err)
                }
            }

            // Step 2: ask the server what's actually still missing.
            try {
                const s = await getConsentStatus({ force: true })
                if (cancelled) return
                if (!s) {
                    setError('Onay durumu alınamadı. Lütfen sayfayı yenileyin.')
                    setLoading(false)
                    return
                }
                if (s.satisfied) {
                    // Nothing to do — flush leftover queue and bounce
                    // back to the app.
                    clearPendingConsents()
                    navigate('/dashboard', { replace: true })
                    return
                }
                setStatus(s)
                setLoading(false)
            } catch (err) {
                if (cancelled) return
                setError(err.message || 'Onay durumu alınamadı.')
                setLoading(false)
            }
        }

        init()
        return () => {
            cancelled = true
        }
    }, [navigate])

    const handleSubmit = async (e) => {
        e.preventDefault()
        if (!accepted || submitting || !status) return
        setSubmitting(true)
        setError('')

        const consents = buildReacceptConsents(status)
        if (consents.length === 0) {
            // Nothing to record (shouldn't happen because we only
            // render the form when status.satisfied is false).
            clearPendingConsents()
            navigate('/dashboard', { replace: true })
            return
        }

        try {
            await recordConsents(consents)
            clearPendingConsents()
            navigate('/dashboard', { replace: true })
        } catch (err) {
            setError(err.message || 'Onay kaydedilemedi. Lütfen tekrar deneyin.')
            setSubmitting(false)
        }
    }

    if (loading) {
        return (
            <div className="min-h-screen bg-background flex items-center justify-center">
                <div className="text-text font-sans">Onay durumu kontrol ediliyor...</div>
            </div>
        )
    }

    if (error && !status) {
        return (
            <div className="min-h-screen bg-background flex items-center justify-center px-6">
                <div className="max-w-md text-center">
                    <AlertTriangle className="w-10 h-10 mx-auto mb-4 text-danger" strokeWidth={1.5} />
                    <p className="text-text font-sans mb-4">{error}</p>
                    <button
                        onClick={() => window.location.reload()}
                        className="btn-primary inline-flex items-center justify-center gap-2"
                    >
                        Yeniden Dene
                    </button>
                </div>
            </div>
        )
    }

    const missing = status?.missing || []
    const outdated = status?.outdated || []
    const revoked = status?.revoked || []

    const needsTypes = Array.from(
        new Set([
            ...missing,
            ...outdated.map((o) => o.type),
            ...revoked,
        ]),
    )

    return (
        <div className="min-h-screen w-full bg-[var(--canvas-bone)] flex items-center justify-center px-6 py-16 md:px-10">
            <div className="w-full max-w-lg">
                <div className="mb-12 text-center">
                    <span className="font-serif font-medium tracking-tight text-2xl leading-none text-[var(--ink-charcoal)]">
                        syncrun
                    </span>
                </div>

                <div className="mb-10 fade-in-up">
                    <span className="eyebrow">Hesabınızı kullanmaya devam etmek için</span>
                    <h1 className="mt-4 font-serif text-h1 text-[var(--ink-charcoal)]">
                        Güncel Onaylar Gerekli
                    </h1>
                    <p className="mt-3 text-text-secondary text-body font-sans leading-[1.55]">
                        Devam edebilmek için aşağıdaki belgeleri okuyup onaylamanız gerekiyor. Daha önce verdiğiniz onaylardan bir kısmı eksik veya güncel değil.
                    </p>
                </div>

                <div className="fade-in-up">
                    {error && (
                        <div className="mb-6 p-4 border border-danger/40 text-danger text-meta font-sans">
                            {error}
                        </div>
                    )}

                    {needsTypes.length > 0 && (
                        <ul className="mb-6 space-y-2 text-meta font-sans text-text-secondary">
                            {needsTypes.map((type) => {
                                const out = outdated.find((o) => o.type === type)
                                return (
                                    <li key={type} className="flex items-start gap-2">
                                        <span className="text-[var(--accent-ochre-ink)] mt-0.5">•</span>
                                        <span>
                                            <Link
                                                to={CONSENT_LINKS[type] || '#'}
                                                target="_blank"
                                                className="text-[var(--ink-charcoal)] underline underline-offset-2 hover:decoration-[var(--accent-ochre-ink)]"
                                            >
                                                {CONSENT_LABELS[type] || type}
                                            </Link>
                                            {out && (
                                                <span className="text-text-tertiary"> (sürüm {out.accepted_version} → {out.current_version})</span>
                                            )}
                                            {missing.includes(type) && !out && (
                                                <span className="text-text-tertiary"> (yeni onay)</span>
                                            )}
                                            {revoked.includes(type) && (
                                                <span className="text-text-tertiary"> (yeniden onay)</span>
                                            )}
                                        </span>
                                    </li>
                                )
                            })}
                        </ul>
                    )}

                    <form onSubmit={handleSubmit}>
                        <label className="flex items-start gap-3 cursor-pointer mb-6">
                            <input
                                type="checkbox"
                                checked={accepted}
                                onChange={(e) => setAccepted(e.target.checked)}
                                className="mt-1 h-4 w-4 accent-[var(--accent-ochre-ink)] cursor-pointer"
                                required
                            />
                            <span className="text-meta font-sans text-text-secondary leading-[1.6]">
                                Yukarıdaki belgeleri okudum; toplantı içeriklerimin yapay zekâ ile işlenmesine ve verilerimin yurt dışındaki servis sağlayıcılara aktarılmasına <strong className="text-[var(--ink-charcoal)]">açık rıza veriyorum</strong>.
                            </span>
                        </label>

                        <button
                            type="submit"
                            disabled={!accepted || submitting}
                            className="btn-primary w-full inline-flex items-center justify-center gap-2"
                        >
                            {submitting ? (
                                <>
                                    <div className="w-4 h-4 border-2 border-[var(--canvas-bone)] border-t-transparent rounded-full animate-spin" />
                                    Kaydediliyor...
                                </>
                            ) : (
                                <>
                                    Onayla ve Devam Et
                                    <ArrowRight className="w-4 h-4" strokeWidth={1.5} />
                                </>
                            )}
                        </button>
                    </form>
                </div>

                <p className="mt-12 text-center text-micro text-text-tertiary font-sans leading-[1.55]">
                    Onay vermek istemiyorsanız hesabınızı kapatabilirsiniz. Daha fazla bilgi için <a href="mailto:support@syncrun.net" className="underline underline-offset-2 hover:text-[var(--ink-charcoal)]">support@syncrun.net</a>.
                </p>
            </div>
        </div>
    )
}
