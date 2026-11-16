import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'

// Render the actual i18n key as the visible text — ChainBadge tests
// assert on the keys (not the human translation) so changing copy
// in tr.json doesn't accidentally break behavioural assertions.
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key) => key }),
}))

import ChainBadge from './ChainBadge'

describe('ChainBadge', () => {
  it('renders empty state when no chain and no method', () => {
    render(<ChainBadge />)
    expect(screen.getByText('chain.noChain')).toBeInTheDocument()
  })

  it('renders method label when chain_assignment_method is set', () => {
    render(
      <ChainBadge
        chainId="chain-1"
        chainAssignmentMethod="auto_score"
        meetingId="m1"
      />,
    )
    expect(screen.getByText('chain.method.auto_score')).toBeInTheDocument()
  })

  it('shows manual_removed badge even when chainId is null (lock survives chain detach)', () => {
    render(
      <ChainBadge
        chainId={null}
        chainAssignmentMethod="manual_removed"
        meetingId="m1"
      />,
    )
    expect(screen.getByText('chain.method.manual_removed')).toBeInTheDocument()
    expect(screen.queryByText('chain.noChain')).not.toBeInTheDocument()
  })

  it('falls back to legacy chain type label when method is missing', () => {
    render(
      <ChainBadge
        chainId="chain-1"
        chainType="calendar_series"
        meetingId="m1"
      />,
    )
    // Legacy fallback returns the hardcoded Turkish label.
    expect(screen.getByText('Tekrarlayan Seri')).toBeInTheDocument()
  })

  it('hides actions menu when no callbacks provided', () => {
    render(
      <ChainBadge
        chainId="chain-1"
        chainAssignmentMethod="auto_score"
        meetingId="m1"
      />,
    )
    expect(screen.queryByLabelText(/manage/i)).not.toBeInTheDocument()
    expect(screen.queryByRole('menu')).not.toBeInTheDocument()
  })

  it('shows actions menu when only onMoveToChain is wired', () => {
    render(
      <ChainBadge
        chainId="chain-1"
        chainAssignmentMethod="auto_score"
        meetingId="m1"
        onMoveToChain={vi.fn()}
      />,
    )
    fireEvent.click(screen.getByTitle('chain.actions.manage'))
    expect(screen.getByText('chain.actions.moveToChain')).toBeInTheDocument()
  })

  it('hides move-to-chain when callback is not provided', () => {
    render(
      <ChainBadge
        chainId="chain-1"
        chainAssignmentMethod="auto_score"
        meetingId="m1"
        onRemoveChain={vi.fn()}
      />,
    )
    fireEvent.click(screen.getByTitle('chain.actions.manage'))
    expect(screen.queryByText('chain.actions.moveToChain')).not.toBeInTheDocument()
  })

  it('opens menu and fires onMoveToChain with meeting id', () => {
    const onMoveToChain = vi.fn()
    render(
      <ChainBadge
        chainId="chain-1"
        chainAssignmentMethod="auto_score"
        meetingId="m-42"
        onMoveToChain={onMoveToChain}
      />,
    )
    fireEvent.click(screen.getByTitle('chain.actions.manage'))
    fireEvent.click(screen.getByText('chain.actions.moveToChain'))
    expect(onMoveToChain).toHaveBeenCalledWith('m-42')
  })

  it('shows move-to-chain even for manual_removed (lets user pick a destination after detach)', () => {
    render(
      <ChainBadge
        chainId={null}
        chainAssignmentMethod="manual_removed"
        meetingId="m1"
        onMoveToChain={vi.fn()}
        onRetryAutoDetect={vi.fn()}
      />,
    )
    fireEvent.click(screen.getByTitle('chain.actions.manage'))
    expect(screen.getByText('chain.actions.moveToChain')).toBeInTheDocument()
    expect(screen.getByText('chain.actions.retryAutoDetect')).toBeInTheDocument()
  })

  it('opens actions menu and fires onRemoveChain', () => {
    const onRemoveChain = vi.fn()
    render(
      <ChainBadge
        chainId="chain-1"
        chainAssignmentMethod="auto_score"
        meetingId="m1"
        onRemoveChain={onRemoveChain}
      />,
    )

    fireEvent.click(screen.getByTitle('chain.actions.manage'))
    fireEvent.click(screen.getByText('chain.actions.removeFromChain'))

    expect(onRemoveChain).toHaveBeenCalledWith('m1')
  })

  it('hides retry-auto-detect for non-locked methods even when callback is wired', () => {
    render(
      <ChainBadge
        chainId="chain-1"
        chainAssignmentMethod="auto_score" // not locked
        meetingId="m1"
        onRemoveChain={vi.fn()}
        onRetryAutoDetect={vi.fn()}
      />,
    )
    fireEvent.click(screen.getByTitle('chain.actions.manage'))
    expect(screen.queryByText('chain.actions.retryAutoDetect')).not.toBeInTheDocument()
  })

  it('shows retry-auto-detect for manual_removed (the only valid retry method)', () => {
    render(
      <ChainBadge
        chainId={null}
        chainAssignmentMethod="manual_removed"
        meetingId="m1"
        onRemoveChain={vi.fn()}
        onRetryAutoDetect={vi.fn()}
      />,
    )
    fireEvent.click(screen.getByTitle('chain.actions.manage'))
    expect(screen.getByText('chain.actions.retryAutoDetect')).toBeInTheDocument()
  })

  it('hides retry-auto-detect for manual_user (matcher would no-op via already-assigned bypass)', () => {
    render(
      <ChainBadge
        chainId="chain-1"
        chainAssignmentMethod="manual_user"
        meetingId="m1"
        onRemoveChain={vi.fn()}
        onRetryAutoDetect={vi.fn()}
      />,
    )
    fireEvent.click(screen.getByTitle('chain.actions.manage'))
    expect(screen.queryByText('chain.actions.retryAutoDetect')).not.toBeInTheDocument()
  })

  it('hides remove action for manual_removed (chain is already detached)', () => {
    render(
      <ChainBadge
        chainId={null}
        chainAssignmentMethod="manual_removed"
        meetingId="m1"
        onRemoveChain={vi.fn()}
        onRetryAutoDetect={vi.fn()}
      />,
    )
    fireEvent.click(screen.getByTitle('chain.actions.manage'))
    expect(screen.queryByText('chain.actions.removeFromChain')).not.toBeInTheDocument()
    expect(screen.getByText('chain.actions.retryAutoDetect')).toBeInTheDocument()
  })

  it('attaches the method tooltip to the badge', () => {
    render(
      <ChainBadge
        chainId="chain-1"
        chainAssignmentMethod="manual_user"
        meetingId="m1"
      />,
    )
    expect(screen.getByText('chain.method.manual_user').closest('span'))
      .toHaveAttribute('title', 'chain.methodTooltip.manual_user')
  })

  it('calls onRetryAutoDetect with meeting id', () => {
    const onRetry = vi.fn()
    render(
      <ChainBadge
        chainId={null}
        chainAssignmentMethod="manual_removed"
        meetingId="m-7"
        onRetryAutoDetect={onRetry}
      />,
    )
    fireEvent.click(screen.getByTitle('chain.actions.manage'))
    fireEvent.click(screen.getByText('chain.actions.retryAutoDetect'))
    expect(onRetry).toHaveBeenCalledWith('m-7')
  })
})
