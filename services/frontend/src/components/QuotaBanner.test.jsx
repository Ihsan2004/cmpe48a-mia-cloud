import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, act, cleanup, fireEvent } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'

// Mock the API module BEFORE importing the provider so the import-time
// reference inside QuotaContext picks up the fake. getMyQuota is the
// only network call the context makes.
const getMyQuotaMock = vi.fn()
vi.mock('../services/api', () => ({
  getMyQuota: () => getMyQuotaMock(),
}))

import { QuotaProvider } from '../context/QuotaContext'
import QuotaBanner from './QuotaBanner'

function renderWith(status) {
  getMyQuotaMock.mockResolvedValueOnce(status)
  return render(
    <MemoryRouter>
      <QuotaProvider authenticated={true}>
        <QuotaBanner />
      </QuotaProvider>
    </MemoryRouter>,
  )
}

describe('QuotaBanner', () => {
  beforeEach(() => {
    getMyQuotaMock.mockReset()
  })
  afterEach(() => {
    cleanup()
  })

  it('renders nothing below the 80% threshold', async () => {
    await act(async () => {
      renderWith({ tier: 'free', used_pct: 45, cycle_resets_at: '2026-06-17T00:00:00Z' })
    })
    expect(screen.queryByTestId('quota-banner')).toBeNull()
    expect(screen.queryByTestId('quota-modal')).toBeNull()
  })

  it('shows the warning banner at 80% and includes reset date', async () => {
    await act(async () => {
      renderWith({ tier: 'free', used_pct: 80, cycle_resets_at: '2026-06-17T00:00:00Z' })
    })
    const banner = screen.getByTestId('quota-banner')
    expect(banner).toBeInTheDocument()
    expect(banner.textContent).toMatch(/%80/)
    expect(banner.textContent).toMatch(/17 Haziran 2026/)
    expect(screen.queryByTestId('quota-modal')).toBeNull()
  })

  it('renders the modal (not the banner) at 100%', async () => {
    await act(async () => {
      renderWith({ tier: 'free', used_pct: 100, cycle_resets_at: '2026-06-17T00:00:00Z' })
    })
    expect(screen.queryByTestId('quota-banner')).toBeNull()
    expect(screen.getByTestId('quota-modal')).toBeInTheDocument()
  })

  it('opens the modal when a quota:exceeded event fires from any endpoint', async () => {
    await act(async () => {
      renderWith({ tier: 'free', used_pct: 10, cycle_resets_at: '2026-06-17T00:00:00Z' })
    })
    // Banner shouldn't render at 10%.
    expect(screen.queryByTestId('quota-banner')).toBeNull()

    // Subsequent /usage/me refetch after 429 — return the same payload
    // so the modal stays open via exceededAt (not via used_pct).
    getMyQuotaMock.mockResolvedValueOnce({
      tier: 'free',
      used_pct: 100,
      cycle_resets_at: '2026-06-17T00:00:00Z',
    })

    await act(async () => {
      window.dispatchEvent(
        new CustomEvent('quota:exceeded', {
          detail: {
            tier: 'free',
            used_pct: 100,
            cycle_resets_at: '2026-06-17T00:00:00Z',
          },
        }),
      )
    })

    expect(screen.getByTestId('quota-modal')).toBeInTheDocument()
  })

  it('dismiss closes the modal', async () => {
    await act(async () => {
      renderWith({ tier: 'free', used_pct: 100, cycle_resets_at: '2026-06-17T00:00:00Z' })
    })
    const modal = screen.getByTestId('quota-modal')
    expect(modal).toBeInTheDocument()

    fireEvent.click(screen.getByTestId('quota-modal-close'))
    expect(screen.queryByTestId('quota-modal')).toBeNull()
  })

  it('internal tier hides both banner and modal regardless of used_pct', async () => {
    await act(async () => {
      renderWith({ tier: 'internal', used_pct: 0, cycle_resets_at: '2026-06-17T00:00:00Z' })
    })
    expect(screen.queryByTestId('quota-banner')).toBeNull()
    expect(screen.queryByTestId('quota-modal')).toBeNull()
  })

  // Issue #39 review fix — dismiss must only suppress the *current*
  // 429 event, not future ones. A second endpoint hitting the quota
  // (different exceededAt timestamp) should re-open the modal so the
  // user is informed every time a new cost action is blocked.
  it('reopens modal when a fresh quota:exceeded event fires after dismiss', async () => {
    await act(async () => {
      renderWith({ tier: 'free', used_pct: 10, cycle_resets_at: '2026-06-17T00:00:00Z' })
    })
    // First 429: modal opens.
    getMyQuotaMock.mockResolvedValueOnce({
      tier: 'free',
      used_pct: 100,
      cycle_resets_at: '2026-06-17T00:00:00Z',
    })
    await act(async () => {
      window.dispatchEvent(
        new CustomEvent('quota:exceeded', {
          detail: {
            tier: 'free',
            used_pct: 100,
            cycle_resets_at: '2026-06-17T00:00:00Z',
          },
        }),
      )
    })
    expect(screen.getByTestId('quota-modal')).toBeInTheDocument()

    // Dismiss.
    fireEvent.click(screen.getByTestId('quota-modal-close'))
    expect(screen.queryByTestId('quota-modal')).toBeNull()

    // Second 429 from a different endpoint — modal MUST reopen.
    getMyQuotaMock.mockResolvedValueOnce({
      tier: 'free',
      used_pct: 100,
      cycle_resets_at: '2026-06-17T00:00:00Z',
    })
    await act(async () => {
      window.dispatchEvent(
        new CustomEvent('quota:exceeded', {
          detail: {
            tier: 'free',
            used_pct: 100,
            cycle_resets_at: '2026-06-17T00:00:00Z',
          },
        }),
      )
    })
    expect(screen.getByTestId('quota-modal')).toBeInTheDocument()
  })
})
