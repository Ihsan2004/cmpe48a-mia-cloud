import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import ConfidenceBadge from './ConfidenceBadge'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k) => k }),
}))

describe('ConfidenceBadge', () => {
  it('renders the high label', () => {
    render(<ConfidenceBadge bucket="high" />)
    expect(screen.getByText('onboarding.teams.confidence.high')).toBeTruthy()
  })
  it('renders the mid label', () => {
    render(<ConfidenceBadge bucket="mid" />)
    expect(screen.getByText('onboarding.teams.confidence.mid')).toBeTruthy()
  })
  it('renders the low label', () => {
    render(<ConfidenceBadge bucket="low" />)
    expect(screen.getByText('onboarding.teams.confidence.low')).toBeTruthy()
  })
  it('falls back to low for unknown bucket', () => {
    render(<ConfidenceBadge bucket={undefined} />)
    expect(screen.getByText('onboarding.teams.confidence.low')).toBeTruthy()
  })
})
