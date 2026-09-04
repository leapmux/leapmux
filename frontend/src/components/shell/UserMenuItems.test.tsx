import { cleanup, fireEvent, render, screen } from '@solidjs/testing-library'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { UserMenuItems } from './UserMenuItems'

const { setShowAboutDialog, openPreferences, isDesktopApp, isAutoAuthenticated, switchMode } = vi.hoisted(() => ({
  setShowAboutDialog: vi.fn(),
  openPreferences: vi.fn(),
  isDesktopApp: { value: false },
  isAutoAuthenticated: { value: false },
  switchMode: vi.fn(async () => {}),
}))

vi.mock('~/context/AuthContext', () => ({
  useAuth: () => ({
    logout: vi.fn(async () => {}),
    user: () => ({ username: 'admin', isAdmin: false }),
  }),
}))

vi.mock('@solidjs/router', () => ({
  useNavigate: () => vi.fn(),
}))

vi.mock('~/components/shell/UserMenuState', () => ({
  setShowAboutDialog,
  openPreferences,
}))

vi.mock('~/lib/systemInfo', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/lib/systemInfo')>()
  return {
    ...actual,
    isDesktopApp: () => isDesktopApp.value,
    isAutoAuthenticated: () => isAutoAuthenticated.value,
  }
})

vi.mock('~/api/platformBridge', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/api/platformBridge')>()
  return {
    ...actual,
    platformBridge: {
      ...actual.platformBridge,
      switchMode,
    },
  }
})

vi.mock('~/components/common/DropdownMenu', () => ({
  DropdownMenuItemContent: (props: { label: string, detail?: string }) => (
    <span>
      <span>{props.label}</span>
      {props.detail ? <span>{props.detail}</span> : null}
    </span>
  ),
}))

beforeEach(() => {
  isDesktopApp.value = false
  isAutoAuthenticated.value = false
  setShowAboutDialog.mockClear()
  openPreferences.mockClear()
  switchMode.mockClear()
})

afterEach(() => {
  cleanup()
})

describe('userMenuItems', () => {
  it('always offers About and Preferences', () => {
    render(() => <UserMenuItems />)
    expect(screen.getByRole('menuitem', { name: /About/ })).toBeInTheDocument()
    expect(screen.getByRole('menuitem', { name: /Preferences/ })).toBeInTheDocument()
  })

  // Log out ends a SESSION, so the question is whether this connection holds
  // one -- not whether the hub runs in solo mode. A solo hub whose account has
  // a password hands its network callers a real session, and hiding the item
  // there would leave them no way to end it.
  it('shows Log out for a session and hides it on a credential-free connection', () => {
    render(() => <UserMenuItems />)
    expect(screen.getByRole('menuitem', { name: 'Log out' })).toBeInTheDocument()
    cleanup()

    isAutoAuthenticated.value = true
    render(() => <UserMenuItems />)
    expect(screen.queryByRole('menuitem', { name: 'Log out' })).toBeNull()
  })

  it('offers Switch mode only in the desktop app (return to the launcher)', async () => {
    render(() => <UserMenuItems />)
    expect(screen.queryByRole('menuitem', { name: /Switch mode/ })).toBeNull()
    cleanup()

    isDesktopApp.value = true
    render(() => <UserMenuItems />)
    const item = screen.getByRole('menuitem', { name: /Switch mode/ })
    fireEvent.click(item)
    await vi.waitFor(() => expect(switchMode).toHaveBeenCalledOnce())
  })

  it('keeps Switch mode in solo desktop (no logout, still return to launcher)', () => {
    // The desktop app reaches its hub over the local socket, which the hub
    // authenticates by existing: no session, so nothing to log out of.
    isAutoAuthenticated.value = true
    isDesktopApp.value = true
    render(() => <UserMenuItems />)
    expect(screen.queryByRole('menuitem', { name: 'Log out' })).toBeNull()
    expect(screen.getByRole('menuitem', { name: /Switch mode/ })).toBeInTheDocument()
    expect(screen.getByRole('menuitem', { name: /About LeapMux Desktop/ })).toBeInTheDocument()
  })
})
