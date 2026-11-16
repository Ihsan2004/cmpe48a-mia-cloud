import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'

const mockNavigate = vi.fn()

vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom')
  return {
    ...actual,
    useNavigate: () => mockNavigate,
    useSearchParams: () => [new URLSearchParams(), () => {}],
  }
})

vi.mock('../services/auth', () => ({
  getStoredUser: () => ({ email: 'test@example.com' }),
}))

vi.mock('../services/api', () => ({
  checkCalendarConnection: vi.fn(),
  getCalendarConnectURL: vi.fn(),
  saveGoogleToken: vi.fn(),
  getProfile: vi.fn(),
}))

vi.mock('../utils/errorParser', () => ({
  getFriendlyErrorMessage: (e) => (e?.message || 'err'),
}))

import * as api from '../services/api'
import ConnectCalendar from './ConnectCalendar'

describe('ConnectCalendar — D-27 auto-redirect to /onboarding/teams', () => {
  beforeEach(() => {
    mockNavigate.mockReset()
    api.checkCalendarConnection.mockReset()
    api.getProfile.mockReset()
  })

  it('redirects to /onboarding/teams when status is pending', async () => {
    api.checkCalendarConnection.mockResolvedValue({ is_calendar_connected: true })
    api.getProfile.mockResolvedValue({ teams_onboarding_status: 'pending' })

    render(<MemoryRouter><ConnectCalendar /></MemoryRouter>)

    await waitFor(() => {
      expect(mockNavigate).toHaveBeenCalledWith('/onboarding/teams', { replace: true })
    })
  })

  it('does NOT redirect when status is completed (RESEARCH Pitfall 6)', async () => {
    api.checkCalendarConnection.mockResolvedValue({ is_calendar_connected: true })
    api.getProfile.mockResolvedValue({ teams_onboarding_status: 'completed' })

    render(<MemoryRouter><ConnectCalendar /></MemoryRouter>)

    // Wait long enough for the async chain to settle.
    await new Promise((r) => setTimeout(r, 50))
    expect(mockNavigate).not.toHaveBeenCalledWith('/onboarding/teams', { replace: true })
  })

  it('does NOT redirect when status is skipped', async () => {
    api.checkCalendarConnection.mockResolvedValue({ is_calendar_connected: true })
    api.getProfile.mockResolvedValue({ teams_onboarding_status: 'skipped' })

    render(<MemoryRouter><ConnectCalendar /></MemoryRouter>)
    await new Promise((r) => setTimeout(r, 50))
    expect(mockNavigate).not.toHaveBeenCalledWith('/onboarding/teams', { replace: true })
  })

  it('falls through to step-3 when getProfile rejects (safer fallback)', async () => {
    api.checkCalendarConnection.mockResolvedValue({ is_calendar_connected: true })
    api.getProfile.mockRejectedValue(new Error('network'))

    render(<MemoryRouter><ConnectCalendar /></MemoryRouter>)
    await new Promise((r) => setTimeout(r, 50))
    expect(mockNavigate).not.toHaveBeenCalledWith('/onboarding/teams', { replace: true })
  })

  it('does NOT redirect when calendar is not connected', async () => {
    api.checkCalendarConnection.mockResolvedValue({ is_calendar_connected: false })

    render(<MemoryRouter><ConnectCalendar /></MemoryRouter>)
    await new Promise((r) => setTimeout(r, 50))
    expect(mockNavigate).not.toHaveBeenCalledWith('/onboarding/teams', { replace: true })
    // getProfile should not even be invoked in this branch.
    expect(api.getProfile).not.toHaveBeenCalled()
  })
})
