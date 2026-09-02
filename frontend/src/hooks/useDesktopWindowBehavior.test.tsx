import type { JSX } from 'solid-js'
import type { SettingDescriptor } from '~/generated/proto/leapmux/v1/settings_pb'
import { cleanup, render } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { desktopShellRefusals, resetDesktopShellStatusForTests } from '~/lib/desktopShellStatus'

const setDesktopBehavior = vi.hoisted(() => vi.fn().mockResolvedValue(undefined))
const isDesktopApp = vi.hoisted(() => vi.fn(() => true))

// Only the invoke is replaced. `parseDesktopBehaviorRefusals` stays REAL,
// because narrowing the rejection is part of what this hook is being tested
// for -- a stubbed narrowing would assert the mock's own answer.
vi.mock('~/api/platformBridge', async importOriginal => ({
  ...(await importOriginal<typeof import('~/api/platformBridge')>()),
  setDesktopBehavior,
}))
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
  resetDesktopShellStatusForTests()
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

  // Silence is the failure mode to avoid here: the tray toggle reads "on"
  // while no icon exists, and the login item is simply not registered. The
  // message has to reach the row the user just moved.
  it('reports a refusal to the row that owns it', async () => {
    setDesktopBehavior.mockRejectedValue([{ setting: 'trayEnabled', message: 'no status-icon library' }])
    mount()
    await settle()

    expect(desktopShellRefusals()).toEqual([{
      key: 'desktop.trayEnabled',
      message: 'no status-icon library',
    }])
  })

  // The two choices fail independently, so a push can be refused twice and
  // each message belongs on its own row.
  it('reports every refusal of one push', async () => {
    setDesktopBehavior.mockRejectedValue([
      { setting: 'trayEnabled', message: 'no status-icon library' },
      { setting: 'startOnLogin', message: 'the system declined' },
    ])
    mount()
    await settle()

    expect(desktopShellRefusals()).toEqual([
      { key: 'desktop.trayEnabled', message: 'no status-icon library' },
      { key: 'desktop.startOnLogin', message: 'the system declined' },
    ])
  })

  it('clears the reported refusal once a push succeeds', async () => {
    setDesktopBehavior.mockRejectedValue([{ setting: 'startOnLogin', message: 'the system declined' }])
    mount()
    await settle()
    expect(desktopShellRefusals()).toHaveLength(1)

    setDesktopBehavior.mockResolvedValue(undefined)
    setTrayEnabled(true)
    await settle()
    expect(desktopShellRefusals()).toEqual([])
  })

  // The debounce collapses a burst, but two pushes further apart can still
  // overlap. A late answer about a payload two states old must not reach the
  // row: it would sit beside a control the user already repaired.
  it('ignores a superseded push', async () => {
    let rejectFirst: ((err: unknown) => void) | undefined
    setDesktopBehavior.mockImplementationOnce(
      () => new Promise((_resolve, reject) => { rejectFirst = reject }),
    )
    mount()
    await settle()
    expect(setDesktopBehavior).toHaveBeenCalledTimes(1)

    // A second push lands and is accepted while the first is still open.
    setDesktopBehavior.mockResolvedValue(undefined)
    setTrayEnabled(true)
    await settle()
    expect(setDesktopBehavior).toHaveBeenCalledTimes(2)

    rejectFirst?.([{ setting: 'trayEnabled', message: 'no status-icon library' }])
    await settle()
    expect(desktopShellRefusals()).toEqual([])
  })

  // A transport failure belongs to no row. Showing it beside a toggle would
  // blame the setting for something that has nothing to do with it.
  it('reports nothing for a failure that identifies no setting', async () => {
    setDesktopBehavior.mockRejectedValue(new Error('ipc closed'))
    mount()
    await settle()
    expect(desktopShellRefusals()).toEqual([])
  })

  it('drops a pending push when the component goes away', async () => {
    mount()
    setTrayEnabled(true)
    cleanup()
    await settle()
    expect(setDesktopBehavior).not.toHaveBeenCalled()
  })

  // A TRANSPORT failure may never have reached the command, so nothing was
  // applied or cached. Recording the payload as delivered would make the hook
  // skip every later run that recomputes it, and the shell would keep the
  // previous behaviour for the rest of the session with no message anywhere.
  it('retries a payload the transport never delivered', async () => {
    setDesktopBehavior.mockRejectedValue(new Error('ipc closed'))
    mount()
    await settle()
    expect(setDesktopBehavior).toHaveBeenCalledTimes(1)

    // The same payload, recomputed. Something else woke the effect -- another
    // tab wrote a device override, or the account settings reloaded.
    setDesktopBehavior.mockResolvedValue(undefined)
    setTrayEnabled(true)
    setTrayEnabled(false)
    await settle()

    expect(setDesktopBehavior).toHaveBeenCalledTimes(2)
    expect(setDesktopBehavior).toHaveBeenLastCalledWith({
      trayEnabled: false,
      trayOnClose: 'tray',
      trayOnMinimize: 'taskbar',
      startOnLogin: false,
      startMinimized: 'window',
    })
  })

  // A REFUSAL is the opposite case: the command ran, applied every step it
  // could and cached the set, so the shell holds this payload. Pushing it
  // again would re-register the login item and rebuild the tray for nothing.
  it('does not retry a payload the shell refused', async () => {
    setDesktopBehavior.mockRejectedValue([{ setting: 'trayEnabled', message: 'no status-icon library' }])
    mount()
    await settle()
    expect(setDesktopBehavior).toHaveBeenCalledTimes(1)

    setTrayEnabled(true)
    setTrayEnabled(false)
    await settle()
    expect(setDesktopBehavior).toHaveBeenCalledTimes(1)
  })

  // The effect must record the newest payload even when it matches the last
  // one delivered. Returning early there leaves an ARMED timer holding the
  // value the user moved away from, and it fires with that superseded value.
  it('sends the current values when a change is undone inside the debounce', async () => {
    mount()
    await settle()
    expect(setDesktopBehavior).toHaveBeenCalledTimes(1)

    // On, then off again, both inside one debounce window.
    setTrayEnabled(true)
    await vi.advanceTimersByTimeAsync(20)
    setTrayEnabled(false)
    await settle()

    // The shell already holds `trayEnabled: false`, so the right answer is no
    // second invoke at all -- and certainly not one carrying `true`.
    expect(setDesktopBehavior).toHaveBeenCalledTimes(1)
  })

  it('sends the last value of a burst that ends somewhere new', async () => {
    mount()
    await settle()
    setDesktopBehavior.mockClear()

    setTrayEnabled(true)
    await vi.advanceTimersByTimeAsync(20)
    setTrayOnClose('quit')
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
})
