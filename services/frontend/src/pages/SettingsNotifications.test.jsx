import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'

// ==================== Mocks ====================

// Vitest hoists vi.mock calls, so variables referenced inside the
// factory must be defined via vi.hoisted().
const { mockApi, toast, searchParamsState, setSearchParamsMock } = vi.hoisted(() => ({
  mockApi: {
    getProfile: vi.fn(),
    updateNotificationPreferences: vi.fn(),
    restoreEmailSubscription: vi.fn(),
    getSlackStatus: vi.fn(),
    startSlackInstall: vi.fn(() => Promise.resolve('https://slack.com/oauth/v2/authorize?state=x')),
    resolveSlackEmail: vi.fn(),
    disconnectSlack: vi.fn(),
  },
  toast: { showSuccess: vi.fn(), showError: vi.fn() },
  searchParamsState: { current: new URLSearchParams() },
  setSearchParamsMock: vi.fn(),
}))

vi.mock('react-router-dom', () => ({
  useSearchParams: () => [searchParamsState.current, setSearchParamsMock],
}))

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key, opts) => (opts ? `${key}:${JSON.stringify(opts)}` : key) }),
}))

vi.mock('../utils/toast', () => toast)

// LoadingState — render a marker so the initial fetch vs. loaded state
// can be distinguished in tests.
vi.mock('../components/LoadingState', () => ({ default: () => <div data-testid="loading" /> }))

vi.mock('../services/api', () => mockApi)

import SettingsNotifications from './SettingsNotifications'

// ==================== Helpers ====================

function resetMocks({ profile, slack }) {
  Object.values(toast).forEach((fn) => fn.mockClear && fn.mockClear())
  setSearchParamsMock.mockClear()
  searchParamsState.current = new URLSearchParams()
  mockApi.getProfile.mockResolvedValue(profile)
  mockApi.getSlackStatus.mockResolvedValue(slack)
  mockApi.updateNotificationPreferences.mockResolvedValue({ ok: true })
  mockApi.restoreEmailSubscription.mockResolvedValue({ ok: true })
  mockApi.resolveSlackEmail.mockResolvedValue({ ok: true, matched_email: 'alt@example.com' })
  mockApi.disconnectSlack.mockResolvedValue({ ok: true })
}

function profileFixture(overrides = {}) {
  return {
    id: 'u1',
    brief_deliver_via_email: false,
    brief_deliver_via_slack: false,
    email_unsubscribed_at: null,
    brief_lead_time_minutes: 10,
    ...overrides,
  }
}

async function waitForLoaded() {
  await waitFor(() => {
    expect(screen.queryByTestId('loading')).toBeNull()
  })
}

// ==================== Initial render / loading ====================

describe('SettingsNotifications — loading', () => {
  beforeEach(() => {
    resetMocks({ profile: profileFixture(), slack: { connected: false } })
  })

  it('shows loading state while fetching', () => {
    render(<SettingsNotifications />)
    expect(screen.getByTestId('loading')).toBeDefined()
  })

  it('fetches profile + slack status in parallel on mount', async () => {
    render(<SettingsNotifications />)
    await waitForLoaded()
    expect(mockApi.getProfile).toHaveBeenCalled()
    expect(mockApi.getSlackStatus).toHaveBeenCalled()
  })

  it('still renders when slack status call fails', async () => {
    mockApi.getSlackStatus.mockRejectedValueOnce(new Error('slack down'))
    render(<SettingsNotifications />)
    await waitForLoaded()
    // Slack card falls back to "not connected" state.
    expect(screen.getByTestId('slack-connect')).toBeDefined()
  })
})

// ==================== Email toggle ====================

describe('SettingsNotifications — email toggle', () => {
  it('toggles email preference via PATCH', async () => {
    resetMocks({ profile: profileFixture(), slack: { connected: false } })
    render(<SettingsNotifications />)
    await waitForLoaded()

    const toggle = screen.getByTestId('email-toggle')
    expect(toggle.getAttribute('aria-checked')).toBe('false')
    fireEvent.click(toggle)

    await waitFor(() => {
      expect(mockApi.updateNotificationPreferences).toHaveBeenCalledWith({ brief_deliver_via_email: true })
    })
    expect(toggle.getAttribute('aria-checked')).toBe('true')
  })

  it('shows restore prompt when unsubscribed + disables toggle', async () => {
    resetMocks({
      profile: profileFixture({ email_unsubscribed_at: '2026-04-21T10:00:00Z' }),
      slack: { connected: false },
    })
    render(<SettingsNotifications />)
    await waitForLoaded()

    const toggle = screen.getByTestId('email-toggle')
    expect(toggle.hasAttribute('disabled')).toBe(true)
    expect(screen.getByTestId('restore-unsubscribe')).toBeDefined()
  })

  it('restore clears unsubscribed_at but keeps toggle off', async () => {
    resetMocks({
      profile: profileFixture({ email_unsubscribed_at: '2026-04-21T10:00:00Z' }),
      slack: { connected: false },
    })
    render(<SettingsNotifications />)
    await waitForLoaded()

    fireEvent.click(screen.getByTestId('restore-unsubscribe'))
    await waitFor(() => {
      expect(mockApi.restoreEmailSubscription).toHaveBeenCalled()
    })

    // Restore prompt goes away; toggle re-enables but stays unchecked.
    await waitFor(() => {
      expect(screen.queryByTestId('restore-unsubscribe')).toBeNull()
    })
    expect(screen.getByTestId('email-toggle').getAttribute('aria-checked')).toBe('false')
  })
})

// ==================== Slack card — three states ====================

describe('SettingsNotifications — slack card', () => {
  it('state A: not connected → shows connect button', async () => {
    resetMocks({ profile: profileFixture(), slack: { connected: false } })
    render(<SettingsNotifications />)
    await waitForLoaded()

    expect(screen.getByTestId('slack-connect')).toBeDefined()
    // Slack toggle must be disabled when not connected (can't opt in
    // until there's an integration to deliver through).
    expect(screen.getByTestId('slack-toggle').hasAttribute('disabled')).toBe(true)
  })

  it('state A: connect button fetches consent URL then navigates', async () => {
    resetMocks({ profile: profileFixture(), slack: { connected: false } })
    const assignSpy = vi.fn()
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: { assign: assignSpy },
    })

    render(<SettingsNotifications />)
    await waitForLoaded()
    fireEvent.click(screen.getByTestId('slack-connect'))
    await waitFor(() => {
      expect(mockApi.startSlackInstall).toHaveBeenCalled()
    })
    await waitFor(() => {
      expect(assignSpy).toHaveBeenCalledWith('https://slack.com/oauth/v2/authorize?state=x')
    })
  })

  it('state B: pending email → shows alt-email form', async () => {
    resetMocks({
      profile: profileFixture(),
      slack: { connected: true, pending_email: true, team_name: 'Test Team' },
    })
    render(<SettingsNotifications />)
    await waitForLoaded()

    expect(screen.getByTestId('slack-pending-email')).toBeDefined()
    // Slack toggle stays disabled while integration is incomplete.
    expect(screen.getByTestId('slack-toggle').hasAttribute('disabled')).toBe(true)
  })

  it('state B: resolve-email submit calls API + transitions to active', async () => {
    resetMocks({
      profile: profileFixture(),
      slack: { connected: true, pending_email: true, team_name: 'Test Team' },
    })
    render(<SettingsNotifications />)
    await waitForLoaded()

    fireEvent.change(screen.getByTestId('slack-alt-email-input'), { target: { value: 'alt@example.com' } })
    fireEvent.click(screen.getByTestId('slack-alt-email-submit'))

    await waitFor(() => {
      expect(mockApi.resolveSlackEmail).toHaveBeenCalledWith('alt@example.com')
    })
    // Card flips to active state after successful resolve.
    await waitFor(() => {
      expect(screen.queryByTestId('slack-pending-email')).toBeNull()
      expect(screen.getByTestId('slack-disconnect')).toBeDefined()
    })
  })

  it('state C: active → shows disconnect button', async () => {
    resetMocks({
      profile: profileFixture({ brief_deliver_via_slack: true }),
      slack: { connected: true, pending_email: false, team_name: 'Test Team', matched_email: 'user@example.com' },
    })
    render(<SettingsNotifications />)
    await waitForLoaded()

    expect(screen.getByTestId('slack-disconnect')).toBeDefined()
    expect(screen.getByTestId('slack-toggle').getAttribute('aria-checked')).toBe('true')
  })

  it('state C: disconnect → confirms + calls API + resets state', async () => {
    resetMocks({
      profile: profileFixture({ brief_deliver_via_slack: true }),
      slack: { connected: true, pending_email: false, team_name: 'Test Team' },
    })
    // Earlier tests may have replaced window.location; ensure confirm
    // is defined before spying. Assigning directly is enough since the
    // handler only calls window.confirm().
    window.confirm = vi.fn().mockReturnValue(true)

    render(<SettingsNotifications />)
    await waitForLoaded()

    fireEvent.click(screen.getByTestId('slack-disconnect'))
    await waitFor(() => {
      expect(mockApi.disconnectSlack).toHaveBeenCalled()
    })
    // After disconnect: back to state A + slack toggle auto-cleared.
    await waitFor(() => {
      expect(screen.getByTestId('slack-connect')).toBeDefined()
      expect(screen.getByTestId('slack-toggle').getAttribute('aria-checked')).toBe('false')
    })
  })

  it('toggles slack preference when connected + email matched', async () => {
    resetMocks({
      profile: profileFixture(),
      slack: { connected: true, pending_email: false, matched_email: 'u@example.com' },
    })
    render(<SettingsNotifications />)
    await waitForLoaded()

    const toggle = screen.getByTestId('slack-toggle')
    expect(toggle.hasAttribute('disabled')).toBe(false)
    fireEvent.click(toggle)

    await waitFor(() => {
      expect(mockApi.updateNotificationPreferences).toHaveBeenCalledWith({ brief_deliver_via_slack: true })
    })
    expect(toggle.getAttribute('aria-checked')).toBe('true')
  })
})

// ==================== OAuth callback toast ====================

describe('SettingsNotifications — OAuth callback handshake', () => {
  it('toasts success on ?slack=connected and strips the param', async () => {
    resetMocks({ profile: profileFixture(), slack: { connected: true, pending_email: false } })
    searchParamsState.current = new URLSearchParams('slack=connected')
    render(<SettingsNotifications />)
    await waitForLoaded()

    expect(toast.showSuccess).toHaveBeenCalled()
    expect(setSearchParamsMock).toHaveBeenCalled()
  })

  it('toasts error on ?slack=error', async () => {
    resetMocks({ profile: profileFixture(), slack: { connected: false } })
    searchParamsState.current = new URLSearchParams('slack=error&error=access_denied')
    render(<SettingsNotifications />)
    await waitForLoaded()
    expect(toast.showError).toHaveBeenCalled()
  })
})
