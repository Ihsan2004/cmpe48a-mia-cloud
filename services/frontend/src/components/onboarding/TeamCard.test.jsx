import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import TeamCard from './TeamCard'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (k, v) => (v ? `${k}:${JSON.stringify(v)}` : k),
  }),
}))

vi.mock('sonner', () => ({
  toast: vi.fn(),
}))

const baseTeam = {
  uiId: 't1',
  name: 'Backend',
  confidence_bucket: 'high',
  top_series: ['Sprint planning'],
  members: [
    {
      uiId: 'm1',
      name: 'Ada',
      title: '',
      email: 'ada@example.com',
      shared_meeting_count: 12,
    },
    {
      uiId: 'm2',
      name: 'Bea',
      title: '',
      email: 'bea@example.com',
      shared_meeting_count: 8,
    },
  ],
}

function renderCard(overrides = {}) {
  const handlers = {
    onRename: vi.fn(),
    onRemoveMember: vi.fn(),
    onUndoRemoveMember: vi.fn(),
    onAddExistingMember: vi.fn(),
    onAddExternalMember: vi.fn(),
    onMemberNameChange: vi.fn(),
    onMemberTitleChange: vi.fn(),
    onMerge: vi.fn(),
    onSplit: vi.fn(),
    onDeleteTeam: vi.fn(),
    onUndoDeleteTeam: vi.fn(),
    candidateMembers: [],
    candidateTeams: [{ uiId: 't2', name: 'Design' }],
    ...overrides,
  }
  const utils = render(<TeamCard team={baseTeam} {...handlers} />)
  return { ...utils, handlers }
}

beforeEach(() => vi.clearAllMocks())

describe('TeamCard', () => {
  it('renders the team name + members', () => {
    renderCard()
    expect(screen.getByText('Backend')).toBeTruthy()
    expect(screen.getByDisplayValue('Ada')).toBeTruthy()
  })

  it('rename: clicking the name shows an input, Enter commits', () => {
    const { handlers } = renderCard()
    fireEvent.click(screen.getByText('Backend'))
    const input = screen.getByLabelText('team name')
    fireEvent.change(input, { target: { value: 'API' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(handlers.onRename).toHaveBeenCalledWith('API')
  })

  it('member remove: fires onRemoveMember + 5s undo toast', async () => {
    const { handlers } = renderCard()
    const { toast } = await import('sonner')
    fireEvent.click(screen.getAllByLabelText('remove member')[0])
    expect(handlers.onRemoveMember).toHaveBeenCalledWith('m1')
    expect(toast).toHaveBeenCalled()
    const opts = toast.mock.calls[0][1]
    expect(opts.duration).toBe(5000)
    expect(opts.action.label).toBeDefined()
  })

  it('overflow menu: shows 4 items', () => {
    renderCard()
    fireEvent.click(screen.getByLabelText('team menu'))
    expect(screen.getByText('onboarding.teams.renameTeam')).toBeTruthy()
    expect(screen.getByText('onboarding.teams.mergeTeam')).toBeTruthy()
    expect(screen.getByText('onboarding.teams.splitTeam')).toBeTruthy()
    expect(screen.getByText('onboarding.teams.deleteTeam')).toBeTruthy()
  })

  it('delete team: fires onDeleteTeam + 5s undo toast', async () => {
    const { handlers } = renderCard()
    const { toast } = await import('sonner')
    fireEvent.click(screen.getByLabelText('team menu'))
    fireEvent.click(screen.getByText('onboarding.teams.deleteTeam'))
    expect(handlers.onDeleteTeam).toHaveBeenCalled()
    expect(toast).toHaveBeenCalled()
    const opts = toast.mock.calls[0][1]
    expect(opts.duration).toBe(5000)
  })

  it('renders confidence badge from confidence_bucket', () => {
    renderCard()
    expect(screen.getByText('onboarding.teams.confidence.high')).toBeTruthy()
  })

  it('renders WhyDisclosure with top_series', () => {
    renderCard()
    fireEvent.click(screen.getByText('onboarding.teams.whyDisclosureLabel'))
    expect(screen.getByText('Sprint planning')).toBeTruthy()
  })
})
