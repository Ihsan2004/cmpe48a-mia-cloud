import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import WhyDisclosure from './WhyDisclosure'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k) => k }),
}))

describe('WhyDisclosure', () => {
  it('renders nothing when topSeries is empty', () => {
    const { container } = render(<WhyDisclosure topSeries={[]} />)
    expect(container.querySelector('details')).toBeNull()
  })
  it('renders nothing when topSeries is missing', () => {
    const { container } = render(<WhyDisclosure topSeries={undefined} />)
    expect(container.querySelector('details')).toBeNull()
  })
  it('renders the summary label + bulleted list', () => {
    render(
      <WhyDisclosure
        topSeries={['Backend standup', 'Sprint planning', 'Design review']}
      />,
    )
    expect(screen.getByText('onboarding.teams.whyDisclosureLabel')).toBeTruthy()
    expect(screen.getByText('Backend standup')).toBeTruthy()
    expect(screen.getByText('Sprint planning')).toBeTruthy()
    expect(screen.getByText('Design review')).toBeTruthy()
  })
})
