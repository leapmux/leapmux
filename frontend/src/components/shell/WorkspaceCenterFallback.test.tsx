import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { WorkspaceCenterFallback } from './WorkspaceCenterFallback'

describe('workspaceCenterFallback', () => {
  it('shows the create-workspace affordance when there is no workspace', () => {
    const onNewWorkspace = vi.fn()
    render(() => (
      <WorkspaceCenterFallback noWorkspace bootstrapTimedOut={false} onNewWorkspace={onNewWorkspace} />
    ))

    fireEvent.click(screen.getByTestId('create-workspace-button'))
    expect(onNewWorkspace).toHaveBeenCalledTimes(1)
    expect(screen.queryByTestId('workspace-bootstrap-failed')).toBeNull()
  })

  it('reports a bootstrap the watchdog gave up on instead of leaving a blank panel', () => {
    render(() => (
      <WorkspaceCenterFallback noWorkspace={false} bootstrapTimedOut onNewWorkspace={() => {}} />
    ))

    expect(screen.getByTestId('workspace-bootstrap-failed')).toBeInTheDocument()
    expect(screen.getByText(/state never arrived/i)).toBeInTheDocument()
    expect(screen.queryByTestId('create-workspace-button')).toBeNull()
  })

  it('renders nothing while a selected workspace might still be arriving', () => {
    const { container } = render(() => (
      <WorkspaceCenterFallback noWorkspace={false} bootstrapTimedOut={false} onNewWorkspace={() => {}} />
    ))

    expect(container.innerHTML).toBe('')
  })
})
