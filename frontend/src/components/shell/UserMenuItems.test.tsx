import type { SoloBusyWork, SwitchModeStartResult } from '~/api/platformBridge'
import { cleanup, fireEvent, render, screen } from '@solidjs/testing-library'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { setSoloSwitchModePrompter } from '~/lib/soloStopConfirm'
import { deferred, settle } from '~/test-support/async'
import { UserMenuItems } from './UserMenuItems'

const {
  beginSwitchMode,
  cancelSwitchMode,
  prompter,
  resolveSwitchModeConfirmation,
  setShowAboutDialog,
  openPreferences,
  isDesktopApp,
  isAutoAuthenticated,
  switchMode,
} = vi.hoisted(() => ({
  beginSwitchMode: vi.fn<() => Promise<SwitchModeStartResult>>(),
  cancelSwitchMode: vi.fn<(switchId: number) => Promise<void>>(async () => {}),
  prompter: vi.fn<(busy: SoloBusyWork) => Promise<boolean>>(),
  resolveSwitchModeConfirmation: vi.fn<(promptId: number, confirm: boolean) => Promise<number | null>>(),
  setShowAboutDialog: vi.fn(),
  openPreferences: vi.fn(),
  isDesktopApp: { value: false },
  isAutoAuthenticated: { value: false },
  switchMode: vi.fn<(switchId?: number) => Promise<void>>(async () => {}),
}))

const BUSY_WORK: SoloBusyWork = {
  agents: [
    {
      tabId: 'agent-1',
      title: 'Refactor auth',
      activeBackgroundTasks: ['Run unit tests'],
      activeChildAgents: [],
    },
  ],
  terminals: [],
}

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
    desktopHub: () => null,
    platformBridge: {
      ...actual.platformBridge,
      beginSwitchMode,
      cancelSwitchMode,
      resolveSwitchModeConfirmation,
      switchMode,
    },
  }
})

vi.mock('~/components/common/DropdownMenu', () => ({
  DropdownMenuItemContent: (props: { label: string, detail?: string }) => (
    <span>
      <span>{props.label}</span>
      {props.detail
        ? <span>{props.detail}</span>
        : null}
    </span>
  ),
}))

beforeEach(() => {
  isDesktopApp.value = false
  isAutoAuthenticated.value = false
  beginSwitchMode.mockReset()
  beginSwitchMode.mockResolvedValue({ kind: 'proceed', switchId: 1 })
  cancelSwitchMode.mockReset()
  cancelSwitchMode.mockResolvedValue(undefined)
  resolveSwitchModeConfirmation.mockReset()
  resolveSwitchModeConfirmation.mockImplementation(async (_promptId, confirm) => confirm ? 2 : null)
  prompter.mockReset()
  prompter.mockResolvedValue(true)
  setSoloSwitchModePrompter(prompter)
  setShowAboutDialog.mockClear()
  openPreferences.mockClear()
  switchMode.mockReset()
  switchMode.mockResolvedValue(undefined)
})

afterEach(() => {
  cleanup()
  setSoloSwitchModePrompter(null)
})

describe('UserMenuItems', () => {
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

  it('skips the fade overlay and switchMode when busy confirmation is cancelled', async () => {
    isDesktopApp.value = true
    beginSwitchMode.mockResolvedValueOnce({ kind: 'confirm', promptId: 10, busyWork: BUSY_WORK })
    prompter.mockResolvedValueOnce(false)
    render(() => <UserMenuItems />)

    const childCountBefore = document.body.children.length
    fireEvent.click(screen.getByRole('menuitem', { name: /Switch mode/ }))
    await settle()

    expect(beginSwitchMode).toHaveBeenCalledOnce()
    expect(prompter).toHaveBeenCalledOnce()
    expect(resolveSwitchModeConfirmation).toHaveBeenCalledExactlyOnceWith(10, false)
    expect(switchMode).not.toHaveBeenCalled()
    expect(document.body.children.length).toBe(childCountBefore)
    expect(document.querySelector('[style*="2147483647"]')).toBeNull()
  })

  it('mounts and cleans up the fade overlay around switchMode when confirmation succeeds', async () => {
    isDesktopApp.value = true
    const pendingSwitch = deferred<void>()
    switchMode.mockReturnValueOnce(pendingSwitch.promise)
    render(() => <UserMenuItems />)

    fireEvent.click(screen.getByRole('menuitem', { name: /Switch mode/ }))
    await vi.waitFor(() => expect(switchMode).toHaveBeenCalledExactlyOnceWith(1))
    expect(document.querySelector('[style*="2147483647"]')).not.toBeNull()

    pendingSwitch.resolve(undefined)
    await vi.waitFor(() => expect(document.querySelector('[style*="2147483647"]')).toBeNull())
    expect(switchMode).toHaveBeenCalledTimes(1)
  })

  it('ignores re-entrant Switch mode clicks while confirmation is pending', async () => {
    isDesktopApp.value = true
    beginSwitchMode.mockResolvedValueOnce({ kind: 'confirm', promptId: 11, busyWork: BUSY_WORK })
    const pendingConfirm = deferred<boolean>()
    prompter.mockReturnValueOnce(pendingConfirm.promise)
    render(() => <UserMenuItems />)

    const item = screen.getByRole('menuitem', { name: /Switch mode/ })
    fireEvent.click(item)
    fireEvent.click(item)
    await settle()

    expect(beginSwitchMode).toHaveBeenCalledOnce()
    expect(prompter).toHaveBeenCalledOnce()

    pendingConfirm.resolve(false)
    await settle()
    expect(switchMode).not.toHaveBeenCalled()
  })

  it('runs switchMode once when two UserMenuItems instances click concurrently', async () => {
    isDesktopApp.value = true
    beginSwitchMode.mockResolvedValue({ kind: 'confirm', promptId: 12, busyWork: BUSY_WORK })
    const pendingConfirm = deferred<boolean>()
    prompter.mockImplementation(() => pendingConfirm.promise)
    const pendingSwitch = deferred<void>()
    switchMode.mockReturnValueOnce(pendingSwitch.promise)

    render(() => (
      <>
        <UserMenuItems />
        <UserMenuItems />
      </>
    ))

    const items = screen.getAllByRole('menuitem', { name: /Switch mode/ })
    expect(items).toHaveLength(2)
    fireEvent.click(items[0]!)
    fireEvent.click(items[1]!)
    await vi.waitFor(() => expect(prompter).toHaveBeenCalledOnce())
    pendingConfirm.resolve(true)

    await vi.waitFor(() => expect(switchMode).toHaveBeenCalledExactlyOnceWith(2))
    expect(document.querySelectorAll('[style*="2147483647"]')).toHaveLength(1)

    pendingSwitch.resolve(undefined)
    await vi.waitFor(() => expect(document.querySelectorAll('[style*="2147483647"]')).toHaveLength(0))
    expect(switchMode).toHaveBeenCalledTimes(1)
  })

  it('catches switchMode rejection cleanly without leaving a fade overlay behind', async () => {
    isDesktopApp.value = true
    switchMode.mockRejectedValueOnce(new Error('switch failed'))
    const unhandled: unknown[] = []
    const onUnhandled = (err: unknown) => {
      unhandled.push(err)
    }
    process.on('unhandledRejection', onUnhandled)
    try {
      render(() => <UserMenuItems />)
      fireEvent.click(screen.getByRole('menuitem', { name: /Switch mode/ }))
      await vi.waitFor(() => expect(switchMode).toHaveBeenCalledOnce())
      await settle()
      expect(document.querySelector('[style*="2147483647"]')).toBeNull()
      expect(unhandled).toEqual([])
    }
    finally {
      process.off('unhandledRejection', onUnhandled)
    }
  })
})
