import { useState, useEffect } from 'react'
import { useTranslation } from 'react-i18next'
import { User, Briefcase, Building, Clock3, Globe2, CheckCircle2, AlertCircle, Unlink, Link as LinkIcon, Loader, Save } from 'lucide-react'
import { getProfile, updateProfile, disconnectGoogleCalendar, getCalendarConnectURL, saveGoogleToken } from '../services/api'
import { getStoredUser, storeUser } from '../services/auth'
import { useSearchParams } from 'react-router-dom'
import { showError, showSuccess } from '../utils/toast'
import LoadingState from '../components/LoadingState'

const durationOptions = [
  { label: '15 min', value: 15 },
  { label: '30 min', value: 30 },
  { label: '45 min', value: 45 },
  { label: '60 min', value: 60 },
]

const timezoneOptions = [
  'UTC',
  'Europe/Istanbul',
  'Europe/London',
  'Europe/Berlin',
  'America/New_York',
  'America/Los_Angeles',
  'Asia/Dubai',
  'Asia/Tokyo',
  'Asia/Singapore',
]

export default function Profile({ onUserUpdate }) {
  const { t, i18n } = useTranslation()
  const [searchParams] = useSearchParams()
  const [form, setForm] = useState({
    full_name: '',
    email: '',
    job_title: '',
    company: '',
    timezone: 'UTC',
    default_meeting_duration: 30,
    work_start_hour: '',
    work_end_hour: '',
    bot_name: 'syncrun! Assistant',
    bot_language: 'auto',
    is_calendar_connected: false,
    google_email: '',
    interface_language: 'tr',
    brief_lead_time_minutes: 10,
    // Soft-hide the bot path — desktop is the only surfaced option.
    // Field stays in state so it round-trips through updateProfile
    // unchanged for users whose DB row still says 'bot'; new defaults
    // here flip to 'desktop' for a clean save.
    recording_method: 'desktop',
  })
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [connecting, setConnecting] = useState(false)
  const [disconnecting, setDisconnecting] = useState(false)
  useEffect(() => {
    loadProfile()

    const refreshToken = searchParams.get('refresh_token')
    if (refreshToken) {
      handleOAuthCallback(refreshToken)
    }
  }, [searchParams])

  const handleSave = async () => {
    if (!form.full_name) return

    setSaving(true)

    try {
      // Save profile settings
      await updateProfile({
        full_name: form.full_name,
        email: form.email,
        job_title: form.job_title || null,
        company: form.company || null,
        timezone: form.timezone || 'UTC',
        default_meeting_duration: form.default_meeting_duration || 30,
        work_start_hour: form.work_start_hour || null,
        work_end_hour: form.work_end_hour || null,
        bot_name: form.bot_name || 'syncrun! Assistant',
        bot_language: form.bot_language || 'tr',
        interface_language: form.interface_language,
        brief_lead_time_minutes: form.brief_lead_time_minutes,
        recording_method: form.recording_method || 'desktop',
      })

      // Update language if changed
      if (form.interface_language !== i18n.language) {
        i18n.changeLanguage(form.interface_language)
        localStorage.setItem('interface_language', form.interface_language)
      }

      const updatedProfile = await getProfile()
      if (onUserUpdate) {
        storeUser(updatedProfile)
        onUserUpdate(updatedProfile)
      }

      showSuccess(t('profile.saved'))
    } catch (err) {
      showError(err)
    } finally {
      setSaving(false)
    }
  }

  const handleOAuthCallback = async (refreshToken) => {
    setConnecting(true)
    try {
      const user = getStoredUser()
      await saveGoogleToken(refreshToken, user?.email)
      window.history.replaceState({}, document.title, '/profile')
      showSuccess(t('profile.calendarConnected'))
      await loadProfile()
    } catch (err) {
      showError(err)
    } finally {
      setConnecting(false)
    }
  }

  const loadProfile = async () => {
    setLoading(true)
    try {
      const profile = await getProfile()
      setForm({
        full_name: profile.full_name || '',
        email: profile.email || '',
        job_title: profile.job_title || '',
        company: profile.company || '',
        timezone: profile.timezone || 'UTC',
        default_meeting_duration: profile.default_meeting_duration || 30,
        work_start_hour: profile.work_start_hour || '',
        work_end_hour: profile.work_end_hour || '',
        bot_name: profile.bot_name || 'syncrun! Assistant',
        bot_language: profile.bot_language || 'tr',
        is_calendar_connected: Boolean(profile.is_calendar_connected),
        google_email: profile.google_email || '',
        interface_language: profile.interface_language || 'tr',
        brief_lead_time_minutes: profile.brief_lead_time_minutes || 10,
        recording_method: profile.recording_method || 'desktop',
      })
    } catch (err) {
      showError(err)
    } finally {
      setLoading(false)
    }
  }

  const handleChange = (field, value) => {
    setForm((prev) => ({ ...prev, [field]: value }))
  }

  const handleConnectCalendar = async () => {
    setConnecting(true)

    try {
      const data = await getCalendarConnectURL()
      if (data.state) {
        localStorage.setItem('oauth_state', data.state)
      }
      window.location.href = data.auth_url
    } catch (err) {
      showError(err)
      setConnecting(false)
    }
  }

  const handleDisconnectCalendar = async () => {
    if (!window.confirm(t('profile.confirmDisconnect') || 'Are you sure you want to disconnect Google Calendar?')) {
      return
    }

    setDisconnecting(true)

    try {
      await disconnectGoogleCalendar()
      setForm((prev) => ({ ...prev, is_calendar_connected: false, google_email: '' }))
      await loadProfile()
    } catch (err) {
      showError(err)
    } finally {
      setDisconnecting(false)
    }
  }

  if (loading) {
    return (
      <div className="flex-1 p-8">
        <LoadingState />
      </div>
    )
  }

  return (
    <div className="flex-1 p-8">
      <div className="flex items-center justify-between mb-8">
        <h1 className="text-4xl font-serif text-text">{t('profile.title')}</h1>
      </div>

      <div className="grid gap-6">
        {/* Personal Info */}
        <div className="card">
          <h2 className="text-xl font-serif text-text mb-4">{t('profile.personalInfo')}</h2>
          <div className="grid gap-4 md:grid-cols-2">
            <div>
              <label className="block text-sm font-serif text-text-secondary mb-2">
                <User className="inline w-4 h-4 mr-2" />
                {t('profile.fullName')}
              </label>
              <input
                type="text"
                value={form.full_name}
                onChange={(e) => handleChange('full_name', e.target.value)}
                className="input-field w-full"
                placeholder={t('profile.fullNamePlaceholder')}
                required
              />
            </div>
            <div>
              <label className="block text-sm font-serif text-text-secondary mb-2">
                <Briefcase className="inline w-4 h-4 mr-2" />
                {t('profile.jobTitle')}
              </label>
              <input
                type="text"
                value={form.job_title}
                onChange={(e) => handleChange('job_title', e.target.value)}
                className="input-field w-full"
                placeholder={t('profile.jobTitlePlaceholder')}
              />
            </div>
            <div>
              <label className="block text-sm font-serif text-text-secondary mb-2">
                <Building className="inline w-4 h-4 mr-2" />
                {t('profile.company')}
              </label>
              <input
                type="text"
                value={form.company}
                onChange={(e) => handleChange('company', e.target.value)}
                className="input-field w-full"
                placeholder={t('profile.companyPlaceholder')}
              />
            </div>
            <div>
              <label className="block text-sm font-serif text-text-secondary mb-2">
                {t('profile.emailReadOnly')}
              </label>
              <input
                type="email"
                value={form.email}
                disabled
                className="input-field w-full bg-background border-border font-sans text-text-tertiary"
                placeholder="email@example.com"
              />
            </div>
          </div>
        </div>

        {/* Preferences */}
        <div className="card">
          <h2 className="text-xl font-serif text-text mb-4">{t('profile.preferences')}</h2>
          <div className="grid gap-4 md:grid-cols-2">
            <div>
              <label className="block text-sm font-serif text-text-secondary mb-2">
                <Globe2 className="inline w-4 h-4 mr-2" />
                {t('profile.timezone')}
              </label>
              <select
                value={form.timezone}
                onChange={(e) => handleChange('timezone', e.target.value)}
                className="input-field w-full"
              >
                {timezoneOptions.map((tz) => (
                  <option key={tz} value={tz}>{tz}</option>
                ))}
              </select>
            </div>
            <div>
              <label className="block text-sm font-serif text-text-secondary mb-2">
                <Clock3 className="inline w-4 h-4 mr-2" />
                {t('profile.defaultDuration')}
              </label>
              <select
                value={form.default_meeting_duration}
                onChange={(e) => handleChange('default_meeting_duration', Number(e.target.value))}
                className="input-field w-full"
              >
                {durationOptions.map((opt) => (
                  <option key={opt.value} value={opt.value}>{opt.label}</option>
                ))}
              </select>
            </div>
            <div className="md:col-span-2">
              <label className="block text-sm font-serif text-text-secondary mb-2">
                <Globe2 className="inline w-4 h-4 mr-2" />
                {t('profile.interfaceLanguage')}
              </label>
              <select
                value={form.interface_language}
                onChange={(e) => handleChange('interface_language', e.target.value)}
                className="input-field w-full"
              >
                <option value="tr">{t('languages.tr')}</option>
                <option value="en">{t('languages.en')}</option>
              </select>
              <p className="mt-1 text-xs font-sans text-text-tertiary">
                {t('profile.interfaceLanguageHint')}
              </p>
            </div>

            {/* Bot name + bot language + recording method dropdowns
                hidden — desktop-only soft-hide. Form state still carries
                the values so updateProfile round-trips them unchanged
                if the bot path is ever revived. */}
          </div>

        </div>
      </div>

      {/* Working Hours */}
      <div className="card mt-6">
        <h2 className="text-xl font-serif text-text mb-4">{t('profile.workingHours')}</h2>
        <div className="grid gap-4 md:grid-cols-2">
          <div>
            <label className="block text-sm font-serif text-text-secondary mb-2">
              {t('profile.startHour')}
            </label>
            <input
              type="time"
              value={form.work_start_hour}
              onChange={(e) => handleChange('work_start_hour', e.target.value)}
              className="input-field w-full"
              placeholder="09:00"
            />
          </div>
          <div>
            <label className="block text-sm font-serif text-text-secondary mb-2">
              {t('profile.endHour')}
            </label>
            <input
              type="time"
              value={form.work_end_hour}
              onChange={(e) => handleChange('work_end_hour', e.target.value)}
              className="input-field w-full"
              placeholder="18:00"
            />
          </div>
        </div>
      </div>

      {/* Brief Settings */}
      <div className="card mt-6">
        <h2 className="text-xl font-serif text-text mb-4">Toplantı Hazırlığı</h2>
        <div className="flex items-center gap-4 mb-5">
          <label className="text-sm text-text-secondary">Brief gönderim süresi (toplantıdan kaç dakika önce):</label>
          <select
            value={form.brief_lead_time_minutes || 10}
            onChange={(e) => handleChange('brief_lead_time_minutes', parseInt(e.target.value))}
            className="bg-surface border border-surface-border rounded-md px-3 py-2 text-sm text-text"
          >
            <option value={5}>5 dakika</option>
            <option value={10}>10 dakika</option>
            <option value={15}>15 dakika</option>
            <option value={30}>30 dakika</option>
            <option value={60}>1 saat</option>
          </select>
        </div>

        {/* Mock "Sıradaki Toplantı - Brief" Card Toggle (visual-only preview) */}
        <div className="pt-5 border-t border-border">
          <label className="flex items-start justify-between gap-4 cursor-pointer">
            <div className="flex-1">
              <div className="text-sm font-serif text-text mb-1">
                Sıradaki Toplantı — Brief kartını göster
              </div>
              <div className="text-micro text-text-tertiary font-sans leading-relaxed">
                Ana sayfada, yaklaşan toplantı için katılımcıları, önceki zincir kararlarını
                (Jira durumlarıyla), kısa özeti ve takip sorularını gösteren önizleme kartı.
                <span className="text-primary"> (Görsel önizleme — mock veri)</span>
              </div>
            </div>
            <input
              type="checkbox"
              checked={localStorage.getItem('mock_brief_card_enabled') === 'true'}
              onChange={(e) => {
                if (e.target.checked) {
                  localStorage.setItem('mock_brief_card_enabled', 'true')
                } else {
                  localStorage.removeItem('mock_brief_card_enabled')
                }
                window.dispatchEvent(new Event('mock_brief_card_changed'))
                // Force re-render
                setForm((f) => ({ ...f }))
              }}
              className="mt-1 w-4 h-4 accent-primary cursor-pointer"
            />
          </label>
        </div>
      </div>

      {/* Google Calendar Integration */}
      <div className="card mt-6">
        <h2 className="text-xl font-serif text-text mb-4">{t('profile.googleCalendar')}</h2>

        <div
          className="rounded-md p-5"
          style={{
            background: form.is_calendar_connected
              ? 'rgba(132, 145, 164, 0.1)'
              : 'rgba(245, 245, 243, 0.08)',
            border: form.is_calendar_connected
              ? '1px solid rgba(132, 145, 164, 0.2)'
              : '1px solid rgba(245, 245, 243, 0.15)',
          }}
        >
          <div className="flex items-start justify-between">
            <div className="flex items-start gap-4">
              {form.is_calendar_connected ? (
                <CheckCircle2 className="w-10 h-10 text-[var(--accent-ochre-ink)] flex-shrink-0" />
              ) : (
                <AlertCircle className="w-10 h-10 text-accent flex-shrink-0" />
              )}
              <div>
                <div className="font-serif text-text text-lg mb-1">
                  {form.is_calendar_connected ? t('profile.connected') : t('profile.notConnected')}
                </div>
                <div className="text-sm font-sans text-text-secondary mb-2">
                  {form.is_calendar_connected
                    ? `${form.google_email || 'Google'} ${t('profile.connectedWith')}`
                    : t('profile.calendarNeeded')
                  }
                </div>
                {form.is_calendar_connected && (
                  <div className="flex flex-wrap gap-2 mt-3">
                    <span className="inline-flex items-center gap-1 px-2 py-1 rounded text-xs font-sans bg-[var(--canvas-muted)] text-[var(--accent-ochre-ink)]">
                      <CheckCircle2 className="w-3 h-3" /> {t('profile.calendarRead')}
                    </span>
                    <span className="inline-flex items-center gap-1 px-2 py-1 rounded text-xs font-sans bg-[var(--canvas-muted)] text-[var(--accent-ochre-ink)]">
                      <CheckCircle2 className="w-3 h-3" /> {t('profile.calendarCreate')}
                    </span>
                  </div>
                )}
              </div>
            </div>

            <div className="flex-shrink-0">
              {form.is_calendar_connected ? (
                <button
                  type="button"
                  disabled={disconnecting}
                  onClick={handleDisconnectCalendar}
                  className="flex items-center gap-2 px-4 py-2 rounded-lg font-serif text-danger transition-all hover:bg-danger/10"
                  style={{
                    background: 'rgba(239, 68, 68, 0.05)',
                    border: '1px solid rgba(239, 68, 68, 0.2)',
                  }}
                >
                  {disconnecting ? (
                    <Loader className="w-4 h-4 animate-spin" />
                  ) : (
                    <Unlink className="w-4 h-4" />
                  )}
                  {disconnecting ? t('profile.disconnecting') : t('profile.disconnect')}
                </button>
              ) : (
                <button
                  type="button"
                  disabled={connecting}
                  onClick={handleConnectCalendar}
                  className="btn-primary flex items-center gap-2 font-serif"
                >
                  {connecting ? (
                    <Loader className="w-4 h-4 animate-spin" />
                  ) : (
                    <LinkIcon className="w-4 h-4" />
                  )}
                  {connecting ? t('profile.connecting') : t('profile.connectGoogle')}
                </button>
              )}
            </div>
          </div>
        </div>

        {!form.is_calendar_connected && (
          <div className="mt-4 text-sm font-sans text-text-tertiary">
            <p className="mb-2">{t('profile.calendarBenefits')}</p>
            <ul className="space-y-1 ml-4">
              <li>• {t('profile.benefit1')}</li>
              <li>• {t('profile.benefit2')}</li>
              <li>• {t('profile.benefit3')}</li>
            </ul>
          </div>
        )}
      </div>

      {/* Save Button */}
      <div className="mt-8 flex justify-end">
        <button
          type="button"
          disabled={saving}
          onClick={handleSave}
          className="btn-primary flex items-center gap-2 font-serif px-8 py-3"
        >
          {saving ? (
            <Loader className="w-4 h-4 animate-spin" />
          ) : (
            <Save className="w-4 h-4" />
          )}
          {saving ? t('profile.saving') : t('profile.saveChanges')}
        </button>
      </div>
    </div>
  )
}
