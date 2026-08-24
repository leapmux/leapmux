import type { JSX } from 'solid-js'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { PreferencesProvider } from '~/context/PreferencesContext'
import { WorkspaceCenterFallback } from './WorkspaceCenterFallback'

// The account tier stays at its defaults; nothing here reads it.
vi.mock('~/api/clients', () => ({
  userClient: {
    listUserSettings: vi.fn().mockResolvedValue({ descriptors: [], values: [] }),
    updateUserSetting: vi.fn().mockResolvedValue({}),
    resetUserSetting: vi.fn().mockResolvedValue({}),
  },
  authClient: {},
}))

/**
 * Mount inside a `PreferencesProvider`, which is where `AppShell` mounts this.
 *
 * The wrapper is not decoration: the theme chooser reads the preference through
 * `useThemeChooser`, which now THROWS outside the provider. It used to fall
 * back to a provider-less device tier — a path that existed for the desktop
 * launcher and is gone, because every stored preference is scoped to an account
 * and a surface with no account has none to show.
 */
function renderFallback(ui: () => JSX.Element) {
  return render(() => <PreferencesProvider>{ui()}</PreferencesProvider>)
}

describe('workspaceCenterFallback', () => {
  it('shows the create-workspace affordance when there is no workspace', () => {
    const onNewWorkspace = vi.fn()
    renderFallback(() => (
      <WorkspaceCenterFallback noWorkspace bootstrapTimedOut={false} onNewWorkspace={onNewWorkspace} />
    ))

    fireEvent.click(screen.getByTestId('create-workspace-button'))
    expect(onNewWorkspace).toHaveBeenCalledTimes(1)
    expect(screen.queryByTestId('workspace-bootstrap-failed')).toBeNull()
  })

  it('reports a bootstrap the watchdog gave up on instead of leaving a blank panel', () => {
    renderFallback(() => (
      <WorkspaceCenterFallback noWorkspace={false} bootstrapTimedOut onNewWorkspace={() => {}} />
    ))

    expect(screen.getByTestId('workspace-bootstrap-failed')).toBeInTheDocument()
    expect(screen.getByText(/state never arrived/i)).toBeInTheDocument()
    expect(screen.queryByTestId('create-workspace-button')).toBeNull()
  })

  it('renders nothing while a selected workspace might still be arriving', () => {
    const { container } = renderFallback(() => (
      <WorkspaceCenterFallback noWorkspace={false} bootstrapTimedOut={false} onNewWorkspace={() => {}} />
    ))

    expect(container.innerHTML).toBe('')
  })

  // A user with no workspace yet is looking at their first screen of the app,
  // so the theme is offered here as well as in Preferences. Not restricted to solo
  // mode: this component is mode-agnostic by design.
  //
  // It is also the ONLY surface outside the Preferences dialog that still
  // carries the chooser: the desktop launcher and the first-run setup page
  // render before an account exists, so they have no preference to write.
  it('offers the theme picker beside the create-workspace affordance', () => {
    renderFallback(() => (
      <WorkspaceCenterFallback noWorkspace bootstrapTimedOut={false} onNewWorkspace={() => {}} />
    ))

    expect(screen.getByTestId('theme-chooser')).toBeInTheDocument()
    expect(screen.getByRole('radiogroup', { name: 'Theme mode' })).toBeInTheDocument()
  })

  it('keeps the theme picker out of the bootstrap-failure state', () => {
    // That arm reports an error and offers a reload. Offering a palette there
    // would read as a remedy for a failure it has nothing to do with.
    renderFallback(() => (
      <WorkspaceCenterFallback noWorkspace={false} bootstrapTimedOut onNewWorkspace={() => {}} />
    ))

    expect(screen.queryByTestId('theme-chooser')).toBeNull()
  })
})
