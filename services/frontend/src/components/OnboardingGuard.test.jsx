import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { MemoryRouter, Routes, Route } from 'react-router-dom'
import { UserContext } from '../contexts/UserContext'
import OnboardingGuard from './OnboardingGuard'

function renderWithUser(user, ui, initialEntries = ['/here']) {
  return render(
    <UserContext.Provider value={user}>
      <MemoryRouter initialEntries={initialEntries}>
        <Routes>
          <Route path="/here" element={ui} />
          <Route path="/teams" element={<div>TeamsPage</div>} />
          <Route path="/onboarding/teams" element={<div>OnboardingPage</div>} />
        </Routes>
      </MemoryRouter>
    </UserContext.Provider>
  )
}

describe('OnboardingGuard — mode=onboarding (D-29 status gate)', () => {
  it('renders children when status is pending', () => {
    renderWithUser({ teams_onboarding_status: 'pending' },
      <OnboardingGuard mode="onboarding"><div>onboarding-kids</div></OnboardingGuard>)
    expect(screen.getByText('onboarding-kids')).toBeTruthy()
  })

  it('redirects to /teams when status is completed', () => {
    renderWithUser({ teams_onboarding_status: 'completed' },
      <OnboardingGuard mode="onboarding"><div>onboarding-kids</div></OnboardingGuard>)
    expect(screen.getByText('TeamsPage')).toBeTruthy()
    expect(screen.queryByText('onboarding-kids')).toBeNull()
  })

  it('redirects to /teams when status is skipped', () => {
    renderWithUser({ teams_onboarding_status: 'skipped' },
      <OnboardingGuard mode="onboarding"><div>onboarding-kids</div></OnboardingGuard>)
    expect(screen.getByText('TeamsPage')).toBeTruthy()
  })

  it('renders null while loading (user is null)', () => {
    const { container } = renderWithUser(null,
      <OnboardingGuard mode="onboarding"><div>onboarding-kids</div></OnboardingGuard>)
    expect(screen.queryByText('onboarding-kids')).toBeNull()
    expect(screen.queryByText('TeamsPage')).toBeNull()
    // Container should be empty (or just the wrapper).
    expect(container.textContent).toBe('')
  })
})

describe('OnboardingGuard — mode=teams (D-29 reverse gate)', () => {
  it('renders children when status is completed', () => {
    renderWithUser({ teams_onboarding_status: 'completed' },
      <OnboardingGuard mode="teams"><div>teams-kids</div></OnboardingGuard>)
    expect(screen.getByText('teams-kids')).toBeTruthy()
  })

  it('renders children when status is skipped', () => {
    renderWithUser({ teams_onboarding_status: 'skipped' },
      <OnboardingGuard mode="teams"><div>teams-kids</div></OnboardingGuard>)
    expect(screen.getByText('teams-kids')).toBeTruthy()
  })

  it('redirects to /onboarding/teams when status is pending', () => {
    renderWithUser({ teams_onboarding_status: 'pending' },
      <OnboardingGuard mode="teams"><div>teams-kids</div></OnboardingGuard>)
    expect(screen.getByText('OnboardingPage')).toBeTruthy()
    expect(screen.queryByText('teams-kids')).toBeNull()
  })

  it('renders null while loading', () => {
    const { container } = renderWithUser(null,
      <OnboardingGuard mode="teams"><div>teams-kids</div></OnboardingGuard>)
    expect(screen.queryByText('teams-kids')).toBeNull()
    expect(container.textContent).toBe('')
  })
})
