import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor, act } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'

const mockNavigate = vi.fn()
vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom')
  return { ...actual, useNavigate: () => mockNavigate }
})

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (k, v) => (v ? `${k}:${JSON.stringify(v)}` : k),
  }),
}))

vi.mock('../services/api', () => ({
  detectTeams: vi.fn(),
  saveTeams: vi.fn(),
  skipOnboarding: vi.fn(),
}))

vi.mock('../utils/toast', () => ({
  showSuccess: vi.fn(),
  showError: vi.fn(),
}))

vi.mock('sonner', () => ({ toast: vi.fn() }))

import * as api from '../services/api'
import * as toastUtils from '../utils/toast'
import OnboardingTeams from './OnboardingTeams'

function setup() {
  return render(
    <MemoryRouter>
      <OnboardingTeams />
    </MemoryRouter>,
  )
}

const sampleDTO = {
  status: 'success',
  empty_reason: '',
  teams: [
    {
      suggested_name: 'Backend',
      confidence_bucket: 'high',
      confidence_score: 0.82,
      top_series: ['Sprint planning'],
      members: [
        { email: 'ada@example.com', shared_meeting_count: 12 },
        { email: 'bea@example.com', shared_meeting_count: 8 },
      ],
    },
  ],
  run_id: 'run-1',
}

beforeEach(() => {
  vi.clearAllMocks()
  sessionStorage.clear()
})

afterEach(() => {
  // Ensure fake timers from any test don't leak into the next (waitFor uses
  // real timers under the hood — fake-timer leakage causes 5s timeouts).
  vi.useRealTimers()
})

describe('OnboardingTeams — staged loading', () => {
  it('renders loadingFetch then transitions to loadingDetect after 600ms', async () => {
    vi.useFakeTimers()
    api.detectTeams.mockReturnValue(new Promise(() => {})) // never resolves
    setup()
    expect(screen.getByText('onboarding.teams.loadingFetch')).toBeTruthy()
    await act(async () => {
      vi.advanceTimersByTime(600)
    })
    expect(screen.getByText('onboarding.teams.loadingDetect')).toBeTruthy()
  })

  it('renders edit page after detect resolves', async () => {
    api.detectTeams.mockResolvedValue(sampleDTO)
    setup()
    await waitFor(() => expect(screen.getByText('Backend')).toBeTruthy())
  })
})

describe('OnboardingTeams — edit page', () => {
  it('PRIVACY-01: shows the KVKK disclosure note', async () => {
    api.detectTeams.mockResolvedValue(sampleDTO)
    setup()
    await waitFor(() => expect(screen.getByTestId('privacy-note')).toBeTruthy())
    expect(screen.getByTestId('privacy-note').textContent).toContain(
      'onboarding.teams.privacyNote',
    )
  })

  it('hydrates from sessionStorage and skips detect when draft exists', () => {
    sessionStorage.setItem(
      'team-onboarding-draft-v1',
      JSON.stringify({
        teams: [
          {
            uiId: 'x',
            name: 'Drafted',
            confidence_bucket: 'mid',
            top_series: [],
            members: [],
          },
        ],
      }),
    )
    setup()
    expect(screen.getByText('Drafted')).toBeTruthy()
    expect(api.detectTeams).not.toHaveBeenCalled()
  })

  it('empty-state: shows the Boş takım ekle CTA when teams is empty', async () => {
    api.detectTeams.mockResolvedValue({
      status: 'degenerate',
      empty_reason: 'insufficient_meetings',
      teams: [],
      run_id: 'r',
    })
    setup()
    await waitFor(() =>
      expect(screen.getByText('onboarding.teams.emptyStateCta')).toBeTruthy(),
    )
  })
})

describe('OnboardingTeams — save', () => {
  it('happy path: Kaydet → Onayla → saveTeams → navigate /teams', async () => {
    api.detectTeams.mockResolvedValue(sampleDTO)
    api.saveTeams.mockResolvedValue({ status: 'completed', teams: [] })
    setup()
    await waitFor(() => screen.getByText('Backend'))
    fireEvent.click(
      screen.getByText((c) => c.startsWith('onboarding.teams.saveButton')),
    )
    fireEvent.click(screen.getByText('onboarding.teams.saveConfirmCta'))
    await waitFor(() =>
      expect(mockNavigate).toHaveBeenCalledWith('/teams'),
    )
    expect(toastUtils.showSuccess).toHaveBeenCalled()
  })

  it('409 ALREADY_COMPLETED: shows alreadySaved toast, does NOT navigate', async () => {
    api.detectTeams.mockResolvedValue(sampleDTO)
    const err = new Error('conflict')
    err.code = 'ALREADY_COMPLETED'
    err.status = 409
    api.saveTeams.mockRejectedValue(err)
    setup()
    await waitFor(() => screen.getByText('Backend'))
    fireEvent.click(
      screen.getByText((c) => c.startsWith('onboarding.teams.saveButton')),
    )
    fireEvent.click(screen.getByText('onboarding.teams.saveConfirmCta'))
    await waitFor(() => expect(toastUtils.showError).toHaveBeenCalled())
    expect(mockNavigate).not.toHaveBeenCalledWith('/teams')
  })
})

describe('OnboardingTeams — skip', () => {
  it('skip: calls skipOnboarding(emptyState=false) when teams present, navigates /dashboard', async () => {
    api.detectTeams.mockResolvedValue(sampleDTO)
    api.skipOnboarding.mockResolvedValue({ status: 'skipped' })
    setup()
    await waitFor(() => screen.getByText('Backend'))
    fireEvent.click(screen.getByText('onboarding.teams.skip'))
    await waitFor(() =>
      expect(api.skipOnboarding).toHaveBeenCalledWith(false),
    )
    expect(mockNavigate).toHaveBeenCalledWith('/dashboard')
  })
})

describe('OnboardingTeams — detect errors', () => {
  it('GOOGLE_AUTH_EXPIRED: navigates to /connect-calendar', async () => {
    const err = new Error('expired')
    err.code = 'GOOGLE_AUTH_EXPIRED'
    api.detectTeams.mockRejectedValue(err)
    setup()
    await waitFor(() =>
      expect(mockNavigate).toHaveBeenCalledWith('/connect-calendar', {
        replace: true,
      }),
    )
  })
})
