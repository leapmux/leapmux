import type { JSX } from 'solid-js'
import type { SettingDescriptor } from '~/generated/proto/leapmux/v1/settings_pb'
import { cleanup, render } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const setDesktopBehavior = vi.hoisted(() => vi.fn().mockResolvedValue(undefined))
const isDesktopApp = vi.hoisted(() => vi.fn(() => true))

vi.mock('~/api/platformBridge', () => ({ setDesktopBehavior }))
vi.mock('~/lib/systemInfo', () => ({ isDesktopApp: () => isDesktopApp() }))

// The two contexts are mocked rather than mounted: this hook's whole job is
// the GATE and the coalescing, and driving those through real providers would
// mean staging RPC replies to move one boolean.
const [loading, setLoading] = createSignal(false)
const [user, setUser] = createSignal<{ id: string } | null>({ id: 'u-1' })
const [descriptors, setDescriptors] = createSignal<SettingDescriptor[]>([{} as SettingDescriptor])
const [trayEnabled, setTrayEnabled] = createSignal(false)
const [trayOnClose, setTrayOnClose] = createSignal('tray')
const [trayOnMinimize, setTrayOnMinimize] = createSignal('taskbar')
const [startOnLogin, setStartOnLogin] = createSignal(false)
const [startMinimized, setStartMinimized] = createSignal('window')
const [diffView, setDiffView] = createSignal('unified')

vi.mock('~/context/AuthContext', () => ({
  useAuth: () => ({ loading, user }),
}))
vi.mock('~/context/PreferencesContext', () => ({
  usePreferences: () => ({
    accountDescriptors: descriptors,
    trayEnabled,
    trayOnClose,
    trayOnMinimize,
    startOnLogin,
    startMinimized,
    // Not read by the hook; present so a test can prove an unrelated change
    // does not wake it.
    diffView,
  }),
}))

const { useDesktopWindowBehavior } = await import('./useDesktopWindowBehavior')

function mount(): void {
  function Probe(): JSX.Element {
    useDesktopWindowBehavior()
    return null
  }
  render(() => <Probe />)
}

/** Run past the push debounce. */
async function settle(): Promise<void> {
  await vi.advanceTimersByTimeAsync(200)
}

beforeEach(() => {
  vi.useFakeTimers()
  setDesktopBehavior.mockClear()
  setDesktopBehavior.mockResolvedValue(undefined)
  isDesktopApp.mockReturnValue(true)
  setLoading(false)
  setUser({ id: 'u-1' })
  setDescriptors([{} as SettingDescriptor])
  setTrayEnabled(false)
  setTrayOnClose('tray')
  setTrayOnMinimize('taskbar')
  setStartOnLogin(false)
  setStartMinimized('window')
  setDiffView('unified')
})

afterEach(() => {
  cleanup()
  vi.useRealTimers()
})

describe('useDesktopWindowBehavior', () => {
  it('pushes nothing outside the desktop app', async () => {
    isDesktopApp.mockReturnValue(false)
    mount()
    setTrayEnabled(true)
    await settle()
    expect(setDesktopBehavior).not.toHaveBeenCalled()
  })

  // The case that protects a registered login item. On mount every tier holds
  // its BUILT-IN default, so a push before the account tier answers would send
  // `startOnLogin: false` and deregister the item the user set, on every
  // single launch.
  // Each gate from its own mount. Flipping them in sequence on one mount
  // would open a state the real provider never publishes -- `loading` false
  // beside a user that has not been cleared yet -- and the push it triggers
  // would be the test's doing, not the hook's.
  it.each([
    ['the account settings have not arrived', () => setDescriptors([])],
    ['the session is still loading', () => setLoading(true)],
    ['nobody is signed in', () => setUser(null)],
  ])('pushes nothing while %s', async (_label, arrange) => {
    arrange()
    mount()
    setTrayEnabled(true)
    await settle()
    expect(setDesktopBehavior).not.toHaveBeenCalled()
  })

  it('pushes the five resolved values once the account settings load', async () => {
    setDescriptors([])
    mount()
    await settle()

    setTrayEnabled(true)
    setTrayOnClose('quit')
    setDescriptors([{} as SettingDescriptor])
    await settle()

    expect(setDesktopBehavior).toHaveBeenCalledTimes(1)
    expect(setDesktopBehavior).toHaveBeenCalledWith({
      trayEnabled: true,
      trayOnClose: 'quit',
      trayOnMinimize: 'taskbar',
      startOnLogin: false,
      startMinimized: 'window',
    })
  })

  it('pushes again on a real change and not on an unrelated preference', async () => {
    mount()
    await settle()
    expect(setDesktopBehavior).toHaveBeenCalledTimes(1)

    setTrayOnMinimize('tray')
    await settle()
    expect(setDesktopBehavior).toHaveBeenCalledTimes(2)

    // An unrelated preference must not reach the shell at all: the effect
    // tracks only the five accessors it reads.
    setDiffView('split')
    await settle()
    expect(setDesktopBehavior).toHaveBeenCalledTimes(2)

    // Re-setting the same value is not a change either.
    setTrayOnMinimize('tray')
    await settle()
    expect(setDesktopBehavior).toHaveBeenCalledTimes(2)
  })

  // `reload()` applies the account values one key at a time, so one settings
  // load wakes this effect once per Desktop key.
  it('collapses one settings load into one invoke', async () => {
    setDescriptors([])
    mount()
    await settle()

    setDescriptors([{} as SettingDescriptor])
    setTrayEnabled(true)
    setTrayOnClose('quit')
    setTrayOnMinimize('tray')
    setStartOnLogin(true)
    setStartMinimized('minimized')
    await settle()

    expect(setDesktopBehavior).toHaveBeenCalledTimes(1)
    expect(setDesktopBehavior).toHaveBeenCalledWith({
      trayEnabled: true,
      trayOnClose: 'quit',
      trayOnMinimize: 'tray',
      startOnLogin: true,
      startMinimized: 'minimized',
    })
  })

  // The tray icon and the login item belong to the machine, not to the
  // browsing session. Signing out to switch accounts must not make the icon
  // vanish and the login item deregister.
  it('pushes nothing after sign-out', async () => {
    setStartOnLogin(true)
    mount()
    await settle()
    expect(setDesktopBehavior).toHaveBeenCalledTimes(1)

    // resetForSignOut returns every tier to its default and the identity goes.
    setUser(null)
    setStartOnLogin(false)
    setTrayEnabled(false)
    await settle()
    expect(setDesktopBehavior).toHaveBeenCalledTimes(1)
  })

  it('logs and does not throw when the shell refuses', async () => {
    setDesktopBehavior.mockRejectedValue(new Error('no status-icon library'))
    mount()
    await settle()
    expect(setDesktopBehavior).toHaveBeenCalledTimes(1)

    // A refusal must not stop a later push: the user may be turning the tray
    // back off, which is the way out of the failure.
    setDesktopBehavior.mockResolvedValue(undefined)
    setTrayEnabled(true)
    await settle()
    expect(setDesktopBehavior).toHaveBeenCalledTimes(2)
  })

  it('drops a pending push when the component goes away', async () => {
    mount()
    setTrayEnabled(true)
    cleanup()
    await settle()
    expect(setDesktopBehavior).not.toHaveBeenCalled()
  })
})
