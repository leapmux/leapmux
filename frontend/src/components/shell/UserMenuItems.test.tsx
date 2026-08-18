import { cleanup, fireEvent, render, screen } from '@solidjs/testing-library'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { UserMenuItems } from './UserMenuItems'

const { setShowAboutDialog, openPreferences, isDesktopApp, isSoloMode, switchMode } = vi.hoisted(() => ({
  setShowAboutDialog: vi.fn(),
  openPreferences: vi.fn(),
  isDesktopApp: { value: false },
  isSoloMode: { value: false },
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
    isSoloMode: () => isSoloMode.value,
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
  DropdownMenuItemContent: (props: { label: string, shortcut?: string }) => (
    <span>
      <span>{props.label}</span>
      {props.shortcut ? <span>{props.shortcut}</span> : null}
    </span>
  ),
}))

beforeEach(() => {
  isDesktopApp.value = false
  isSoloMode.value = false
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

  it('shows Log out on a hub session and hides it in solo', () => {
    render(() => <UserMenuItems />)
    expect(screen.getByRole('menuitem', { name: 'Log out' })).toBeInTheDocument()
    cleanup()

    isSoloMode.value = true
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
    isSoloMode.value = true
    isDesktopApp.value = true
    render(() => <UserMenuItems />)
    expect(screen.queryByRole('menuitem', { name: 'Log out' })).toBeNull()
    expect(screen.getByRole('menuitem', { name: /Switch mode/ })).toBeInTheDocument()
    expect(screen.getByRole('menuitem', { name: /About LeapMux Desktop/ })).toBeInTheDocument()
  })
})
