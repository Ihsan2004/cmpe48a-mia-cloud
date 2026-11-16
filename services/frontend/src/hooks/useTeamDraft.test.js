import { describe, it, expect, beforeEach, vi } from 'vitest'
import { renderHook, act } from '@testing-library/react'
import { useTeamDraft } from './useTeamDraft'

describe('useTeamDraft', () => {
  beforeEach(() => {
    sessionStorage.clear()
    vi.useFakeTimers()
  })

  it('returns initial state when no draft exists', () => {
    const { result } = renderHook(() => useTeamDraft({ teams: [] }))
    expect(result.current.state).toEqual({ teams: [] })
    expect(result.current.hasDraft).toBe(false)
  })

  it('hydrates from sessionStorage on mount when draft exists', () => {
    sessionStorage.setItem(
      'team-onboarding-draft-v1',
      JSON.stringify({ teams: [{ uiId: 'a', name: 'Backend', members: [] }] }),
    )
    const { result } = renderHook(() => useTeamDraft({ teams: [] }))
    expect(result.current.state.teams).toHaveLength(1)
    expect(result.current.state.teams[0].name).toBe('Backend')
    expect(result.current.hasDraft).toBe(true)
  })

  it('writes to sessionStorage after 200ms debounce', () => {
    const { result } = renderHook(() => useTeamDraft({ teams: [] }))
    act(() => {
      result.current.setState({ teams: [{ uiId: 'x', name: 'New', members: [] }] })
    })
    // Before 200ms — nothing written yet.
    expect(sessionStorage.getItem('team-onboarding-draft-v1')).toBeNull()
    act(() => { vi.advanceTimersByTime(200) })
    const stored = JSON.parse(sessionStorage.getItem('team-onboarding-draft-v1'))
    expect(stored.teams[0].name).toBe('New')
  })

  it('debounces rapid setState calls to a single write', () => {
    const { result } = renderHook(() => useTeamDraft({ teams: [] }))
    act(() => {
      result.current.setState({ teams: [{ uiId: '1', name: 'A', members: [] }] })
      result.current.setState({ teams: [{ uiId: '1', name: 'B', members: [] }] })
      result.current.setState({ teams: [{ uiId: '1', name: 'C', members: [] }] })
    })
    act(() => { vi.advanceTimersByTime(200) })
    const stored = JSON.parse(sessionStorage.getItem('team-onboarding-draft-v1'))
    expect(stored.teams[0].name).toBe('C')
  })

  it('clearDraft removes the sessionStorage key', () => {
    sessionStorage.setItem('team-onboarding-draft-v1', JSON.stringify({ teams: [{}] }))
    const { result } = renderHook(() => useTeamDraft({ teams: [] }))
    act(() => { result.current.clearDraft() })
    expect(sessionStorage.getItem('team-onboarding-draft-v1')).toBeNull()
  })

  it('recovers gracefully from corrupt sessionStorage', () => {
    sessionStorage.setItem('team-onboarding-draft-v1', '{not-json')
    const { result } = renderHook(() => useTeamDraft({ teams: [] }))
    expect(result.current.state).toEqual({ teams: [] })
    expect(result.current.hasDraft).toBe(false)
  })
})
