import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'

// Stub i18n — return the key so we can assert by key text.
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k) => k }),
}))

import Sidebar from './Sidebar'

function renderWithRouter(props) {
  return render(
    <MemoryRouter>
      <Sidebar
        currentLanguage="tr"
        onLanguageChange={() => {}}
        onLogout={() => {}}
        collapsed={false}
        onToggle={() => {}}
        {...props}
      />
    </MemoryRouter>
  )
}

describe('Sidebar — Phase 7 Takımlar entry', () => {
  it('hides Takımlar when user.teams_onboarding_status is pending', () => {
    renderWithRouter({ user: { teams_onboarding_status: 'pending' } })
    expect(screen.queryByText('sidebar.teams')).toBeNull()
  })

  it('shows Takımlar when status is completed', () => {
    renderWithRouter({ user: { teams_onboarding_status: 'completed' } })
    expect(screen.getByText('sidebar.teams')).toBeTruthy()
  })

  it('shows Takımlar when status is skipped', () => {
    renderWithRouter({ user: { teams_onboarding_status: 'skipped' } })
    expect(screen.getByText('sidebar.teams')).toBeTruthy()
  })

  it('shows Takımlar when status is empty_state_skipped', () => {
    renderWithRouter({ user: { teams_onboarding_status: 'empty_state_skipped' } })
    expect(screen.getByText('sidebar.teams')).toBeTruthy()
  })

  it('hides Takımlar when user is null', () => {
    renderWithRouter({ user: null })
    expect(screen.queryByText('sidebar.teams')).toBeNull()
  })

  it('hides Takımlar when status field is undefined (Pitfall 2 defense)', () => {
    renderWithRouter({ user: { id: '123' /* no teams_onboarding_status */ } })
    expect(screen.queryByText('sidebar.teams')).toBeNull()
  })
})

describe('Sidebar — baseline nav items', () => {
  it('renders home, past meetings, flows, notifications', () => {
    renderWithRouter({ user: { teams_onboarding_status: 'pending' } })
    expect(screen.getByText('sidebar.home')).toBeTruthy()
    expect(screen.getByText('sidebar.pastMeetings')).toBeTruthy()
    expect(screen.getByText('sidebar.flows')).toBeTruthy()
    expect(screen.getByText('sidebar.notifications')).toBeTruthy()
  })
})
