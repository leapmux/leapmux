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

  // A user with no workspace yet is looking at their first screen of the app,
  // so the theme is offered here as well as in Preferences. Not restricted to solo
  // mode: this component is mode-agnostic by design.
  it('offers the theme picker beside the create-workspace affordance', () => {
    render(() => (
      <WorkspaceCenterFallback noWorkspace bootstrapTimedOut={false} onNewWorkspace={() => {}} />
    ))

    expect(screen.getByTestId('theme-chooser')).toBeInTheDocument()
    expect(screen.getByRole('radiogroup', { name: 'Theme mode' })).toBeInTheDocument()
  })

  it('keeps the theme picker out of the bootstrap-failure state', () => {
    // That arm reports an error and offers a reload. Offering a palette there
    // would read as a remedy for a failure it has nothing to do with.
    render(() => (
      <WorkspaceCenterFallback noWorkspace={false} bootstrapTimedOut onNewWorkspace={() => {}} />
    ))

    expect(screen.queryByTestId('theme-chooser')).toBeNull()
  })
})
