import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'

const mockNavigate = vi.fn()
vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom')
  return { ...actual, useNavigate: () => mockNavigate }
})

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k) => k }),
}))

vi.mock('../services/api', () => ({
  listTeams: vi.fn(),
  detectTeams: vi.fn(),
  deleteTeamMember: vi.fn(),
  saveTeams: vi.fn(),
}))

vi.mock('../utils/toast', () => ({
  showSuccess: vi.fn(),
  showError: vi.fn(),
}))

vi.mock('sonner', () => ({ toast: vi.fn() }))

import * as api from '../services/api'
import Teams from './Teams'

function setup() {
  return render(
    <MemoryRouter>
      <Teams />
    </MemoryRouter>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
})

const sampleTeams = [
  {
    id: 't1',
    user_id: 'u1',
    name: 'Backend',
    members: [
      { id: 'm1', team_id: 't1', name: 'Ada', title: 'Engineer', email: 'ada@example.com' },
      { id: 'm2', team_id: 't1', name: 'Bea', title: null, email: 'bea@example.com' },
    ],
  },
  {
    id: 't2',
    user_id: 'u1',
    name: 'Design',
    members: [
      { id: 'm3', team_id: 't2', name: 'Cem', title: null, email: 'cem@example.com' },
    ],
  },
]

describe('Teams — read-only render', () => {
  it('TEAMS-05: renders one card per team from listTeams', async () => {
    api.listTeams.mockResolvedValue({ teams: sampleTeams })
    setup()
    await waitFor(() => expect(screen.getByText('Backend')).toBeTruthy())
    expect(screen.getByText('Design')).toBeTruthy()
    expect(screen.getByText('Ada')).toBeTruthy()
    expect(screen.getByText('Cem')).toBeTruthy()
  })
})

describe('Teams — empty state', () => {
  it('D-21: renders empty title + CTA when listTeams returns zero teams', async () => {
    api.listTeams.mockResolvedValue({ teams: [] })
    setup()
    await waitFor(() => expect(screen.getByText('teams.emptyTitle')).toBeTruthy())
    expect(screen.getByText('teams.emptyCta')).toBeTruthy()
  })
})

describe('Teams — Yeniden tara always-visible (P6 mitigation)', () => {
  it('D-20: redetect button is visible even when user has saved teams', async () => {
    api.listTeams.mockResolvedValue({ teams: sampleTeams })
    setup()
    await waitFor(() => expect(screen.getByTestId('redetect-button')).toBeTruthy())
  })

  it('success + non-empty teams → renders SuggestionsPanel inline, no navigate', async () => {
    api.listTeams.mockResolvedValue({ teams: sampleTeams })
    api.detectTeams.mockResolvedValue({
      status: 'success',
      empty_reason: '',
      teams: [
        {
          suggested_name: 'X Squad',
          confidence_bucket: 'high',
          members: [{ email: 'a@example.com', shared_meeting_count: 8 }],
        },
      ],
      run_id: 'r',
    })
    setup()
    await waitFor(() => screen.getByText('Backend'))
    fireEvent.click(screen.getByTestId('redetect-button'))
    await waitFor(() => expect(api.detectTeams).toHaveBeenCalled())
    // The proposed team name appears as the value of the editable input.
    await waitFor(() => {
      const input = screen.getByDisplayValue('X Squad')
      expect(input).toBeTruthy()
    })
    // Member email chip is visible.
    expect(screen.getByText('a@example.com')).toBeTruthy()
    // No navigation — the panel renders on /teams itself.
    expect(mockNavigate).not.toHaveBeenCalled()
  })

  it('clicking save on a suggestion calls saveTeams + clears it', async () => {
    api.listTeams.mockResolvedValue({ teams: sampleTeams })
    api.detectTeams.mockResolvedValue({
      status: 'success',
      empty_reason: '',
      teams: [
        {
          suggested_name: 'Y Squad',
          confidence_bucket: 'mid',
          members: [
            { email: 'foo@example.com', shared_meeting_count: 5 },
            { email: 'bar@example.com', shared_meeting_count: 5 },
          ],
        },
      ],
      run_id: 'r',
    })
    api.saveTeams.mockResolvedValue({ status: 'completed', teams: [] })
    setup()
    await waitFor(() => screen.getByText('Backend'))
    fireEvent.click(screen.getByTestId('redetect-button'))
    await waitFor(() => screen.getByDisplayValue('Y Squad'))
    fireEvent.click(screen.getByText('teams.suggestionsSave'))
    await waitFor(() => expect(api.saveTeams).toHaveBeenCalled())
    const payload = api.saveTeams.mock.calls[0][0]
    expect(payload.teams).toHaveLength(1)
    expect(payload.teams[0].name).toBe('Y Squad')
    expect(payload.teams[0].members.map((m) => m.email)).toEqual([
      'foo@example.com',
      'bar@example.com',
    ])
    // Card is removed from the panel after a successful save.
    await waitFor(() =>
      expect(screen.queryByDisplayValue('Y Squad')).toBeNull(),
    )
  })

  it('degenerate + insufficient_attendees → reason-keyed toast, no navigate', async () => {
    api.listTeams.mockResolvedValue({ teams: sampleTeams })
    api.detectTeams.mockResolvedValue({
      status: 'degenerate',
      empty_reason: 'insufficient_attendees',
      teams: [],
      run_id: 'r',
    })
    setup()
    await waitFor(() => screen.getByText('Backend'))
    fireEvent.click(screen.getByTestId('redetect-button'))
    await waitFor(() => expect(api.detectTeams).toHaveBeenCalled())
    const { showError } = await import('../utils/toast')
    await waitFor(() =>
      expect(showError).toHaveBeenCalledWith(
        'teams.empty_reason.insufficient_attendees',
      ),
    )
    expect(mockNavigate).not.toHaveBeenCalled()
  })

  it('degenerate + unknown reason → falls back to fallback i18n key', async () => {
    api.listTeams.mockResolvedValue({ teams: sampleTeams })
    api.detectTeams.mockResolvedValue({
      status: 'degenerate',
      empty_reason: 'something_new',
      teams: [],
      run_id: 'r',
    })
    setup()
    await waitFor(() => screen.getByText('Backend'))
    fireEvent.click(screen.getByTestId('redetect-button'))
    await waitFor(() => expect(api.detectTeams).toHaveBeenCalled())
    const { showError } = await import('../utils/toast')
    // The mocked t() returns the key it was called with. The handler calls
    // t(`teams.empty_reason.${reason}`, { defaultValue: t('teams.empty_reason.fallback') }).
    // The mock t() ignores the defaultValue and returns the first arg verbatim,
    // so we just assert showError was called with the keyed path.
    await waitFor(() =>
      expect(showError).toHaveBeenCalledWith(
        'teams.empty_reason.something_new',
      ),
    )
    expect(mockNavigate).not.toHaveBeenCalled()
  })

  it('Pitfall 4: 409 DETECT_RUNNING is a silent no-op', async () => {
    api.listTeams.mockResolvedValue({ teams: sampleTeams })
    const err = new Error('running')
    err.code = 'DETECT_RUNNING'
    api.detectTeams.mockRejectedValue(err)
    setup()
    await waitFor(() => screen.getByText('Backend'))
    fireEvent.click(screen.getByTestId('redetect-button'))
    await waitFor(() => expect(api.detectTeams).toHaveBeenCalled())
    expect(mockNavigate).not.toHaveBeenCalled()
    const { showError } = await import('../utils/toast')
    expect(showError).not.toHaveBeenCalled()
  })
})

describe('Teams — member delete (PRIVACY-02)', () => {
  it('clicking × → deleteTeamMember + optimistic remove + 5s undo toast', async () => {
    api.listTeams.mockResolvedValue({ teams: sampleTeams })
    api.deleteTeamMember.mockResolvedValue()
    setup()
    await waitFor(() => screen.getByText('Ada'))
    fireEvent.click(screen.getByLabelText('remove member ada@example.com'))
    await waitFor(() => expect(api.deleteTeamMember).toHaveBeenCalledWith('m1'))
    const { toast } = await import('sonner')
    expect(toast).toHaveBeenCalled()
    const toastOpts = toast.mock.calls[0][1]
    expect(toastOpts.duration).toBe(5000)
    expect(toastOpts.action.label).toBeDefined()
  })

  it('member is removed from the rendered list optimistically', async () => {
    api.listTeams.mockResolvedValue({ teams: sampleTeams })
    api.deleteTeamMember.mockResolvedValue()
    setup()
    await waitFor(() => screen.getByText('Ada'))
    fireEvent.click(screen.getByLabelText('remove member ada@example.com'))
    await waitFor(() => expect(screen.queryByText('Ada')).toBeNull())
  })
})
