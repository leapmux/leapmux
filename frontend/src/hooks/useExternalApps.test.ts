import type { ExternalApp } from '~/api/platformBridge'
import { createRoot, createSignal } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { _resetExternalAppCacheForTests } from '~/lib/externalApps'
import { editorApp, fileManagerApp } from '~/test-support/externalAppFixtures'
import { useExternalApps } from './useExternalApps'

const { listAppsMock, openInExternalAppMock, showWarnToastMock, prefs } = vi.hoisted(() => ({
  listAppsMock: vi.fn(),
  openInExternalAppMock: vi.fn(),
  showWarnToastMock: vi.fn(),
  prefs: {
    preferredExternalAppId: vi.fn<() => string | undefined>(),
    setPreferredExternalAppId: vi.fn<(id: string | undefined) => void>(),
  },
}))

vi.mock('~/api/platformBridge', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/api/platformBridge')>()
  return {
    ...actual,
    platformBridge: {
      ...actual.platformBridge,
      listExternalApps: (refresh?: boolean) => listAppsMock(refresh ?? false),
      openInExternalApp: (...args: unknown[]) => openInExternalAppMock(...args),
    },
  }
})

vi.mock('~/components/common/Toast', async importOriginal => ({
  ...await importOriginal<typeof import('~/components/common/Toast')>(),
  showWarnToastWithLoggedCause: (...args: unknown[]) => showWarnToastMock(...args),
}))

// The hook reads the pin through the context. Only the two members it touches
// need to behave, and both are spies so a test can assert the WRITE as well as
// the read.
vi.mock('~/context/PreferencesContext', async importOriginal => ({
  ...await importOriginal<typeof import('~/context/PreferencesContext')>(),
  usePreferences: () => prefs,
}))

// The repair belongs to the chat and has its own test. Stubbing it keeps this
// file off `requestAnimationFrame` and off the chat's DOM contract.
vi.mock('~/components/chat/chatScrollPreserve', () => ({
  withChatScrollPreserved: (work: () => Promise<void>) => work(),
}))

const VSCODE = editorApp('vscode', 'Visual Studio Code')
const ZED = editorApp('zed', 'Zed')
const FINDER = fileManagerApp()

beforeEach(() => {
  listAppsMock.mockReset()
  listAppsMock.mockResolvedValue([])
  openInExternalAppMock.mockReset()
  // The bridge declares `Promise<void>`, and the launch path attaches a catch.
  openInExternalAppMock.mockResolvedValue(undefined)
  showWarnToastMock.mockReset()
  prefs.preferredExternalAppId.mockReset()
  prefs.preferredExternalAppId.mockReturnValue(undefined)
  prefs.setPreferredExternalAppId.mockReset()
  _resetExternalAppCacheForTests()
})

afterEach(() => {
  _resetExternalAppCacheForTests()
})

/**
 * Mount one hook inside its own root, and hand back a disposer.
 *
 * `createRoot` rather than a rendered component: the hook owns no markup, and
 * every behavior here is about accessors and the shared module signal.
 */
function mount(enabled = true) {
  let dispose = () => {}
  const apps = createRoot((d) => {
    dispose = d
    return useExternalApps(() => enabled)
  })
  return { apps, dispose }
}

/** Let the probe's promise chain settle. */
async function settle() {
  await Promise.resolve()
  await Promise.resolve()
  await Promise.resolve()
}

describe('useExternalApps probing', () => {
  it('asks the sidecar once the surface is enabled', async () => {
    listAppsMock.mockResolvedValue([VSCODE])
    const { apps, dispose } = mount()

    await settle()

    expect(listAppsMock).toHaveBeenCalledTimes(1)
    expect(apps.apps().map(a => a.id)).toEqual(['vscode'])
    dispose()
  })

  it('asks nothing while the surface is disabled', async () => {
    const { apps, dispose } = mount(false)

    await settle()

    expect(listAppsMock).not.toHaveBeenCalled()
    expect(apps.apps()).toEqual([])
    dispose()
  })

  it('offers no application when the sidecar cannot answer, and does not rethrow', async () => {
    listAppsMock.mockRejectedValue(new Error('sidecar is gone'))
    const { apps, dispose } = mount()

    await settle()

    // Solid re-throws a rejected resource from its accessor, and this accessor
    // is read inside menu JSX, so an escape here would replace the whole shell
    // with the route's error boundary instead of hiding one menu item.
    expect(apps.apps()).toEqual([])
    dispose()
  })

  it('sorts by display name', async () => {
    listAppsMock.mockResolvedValue([ZED, FINDER, VSCODE])
    const { apps, dispose } = mount()

    await settle()

    expect(apps.apps().map(a => a.displayName)).toEqual(['Finder', 'Visual Studio Code', 'Zed'])
    dispose()
  })
})

describe('useExternalApps sharing one list', () => {
  // The regression this exists for: each surface used to keep its own resource,
  // so "Refresh app list" re-fetched only the copy belonging to the menu the
  // user clicked in. The title bar's button probes once and never again, so it
  // went on naming an editor the user had just uninstalled -- and launching it
  // -- for the life of the page.
  it('lets a refresh on one instance reach another instance', async () => {
    listAppsMock.mockResolvedValue([VSCODE, ZED])
    const first = mount()
    const second = mount()
    await settle()
    expect(second.apps.apps().map(a => a.id)).toEqual(['vscode', 'zed'])

    listAppsMock.mockResolvedValue([ZED])
    await first.apps.refresh()

    expect(second.apps.apps().map(a => a.id)).toEqual(['zed'])
    first.dispose()
    second.dispose()
  })

  it('probes once for two instances', async () => {
    listAppsMock.mockResolvedValue([VSCODE])
    const first = mount()
    const second = mount()

    await settle()

    expect(listAppsMock).toHaveBeenCalledTimes(1)
    expect(second.apps.apps()).toHaveLength(1)
    first.dispose()
    second.dispose()
  })

  // A probe that started before the user asked for a refresh describes the
  // machine as it WAS. Letting it land last would silently undo the refresh.
  it('ignores a probe that a later refresh superseded', async () => {
    let settleStale: (apps: ExternalApp[]) => void = () => {}
    listAppsMock.mockImplementationOnce(
      () => new Promise<ExternalApp[]>((resolve) => { settleStale = resolve }),
    )
    const { apps, dispose } = mount()
    await settle()

    listAppsMock.mockResolvedValue([ZED])
    await apps.refresh()
    expect(apps.apps().map(a => a.id)).toEqual(['zed'])

    // The first probe answers only now, with the pre-refresh machine.
    settleStale([VSCODE, ZED])
    await settle()

    expect(apps.apps().map(a => a.id)).toEqual(['zed'])
    dispose()
  })
})

describe('useExternalApps naming the remembered application', () => {
  it('names the pin while it is still detected', async () => {
    listAppsMock.mockResolvedValue([VSCODE, ZED])
    prefs.preferredExternalAppId.mockReturnValue('zed')
    const { apps, dispose } = mount()

    await settle()

    expect(apps.preferred()?.id).toBe('zed')
    expect(apps.preferredId()).toBe('zed')
    dispose()
  })

  // Answering undefined rather than falling back: a surface that only NAMES the
  // remembered application must not silently name a different one.
  it('answers undefined when the pin names an application that is gone', async () => {
    listAppsMock.mockResolvedValue([VSCODE])
    prefs.preferredExternalAppId.mockReturnValue('zed')
    const { apps, dispose } = mount()

    await settle()

    expect(apps.preferred()).toBeUndefined()
    dispose()
  })
})

describe('useExternalApps launching', () => {
  it('remembers the pick and opens the directory', async () => {
    listAppsMock.mockResolvedValue([VSCODE])
    const { apps, dispose } = mount()
    await settle()

    apps.launch('vscode', '/home/me/repo')

    expect(prefs.setPreferredExternalAppId).toHaveBeenCalledWith('vscode')
    expect(openInExternalAppMock).toHaveBeenCalledWith('vscode', '/home/me/repo')
    dispose()
  })

  it('does nothing without a directory', async () => {
    listAppsMock.mockResolvedValue([VSCODE])
    const { apps, dispose } = mount()
    await settle()

    apps.launch('vscode', '')

    expect(openInExternalAppMock).not.toHaveBeenCalled()
    expect(prefs.setPreferredExternalAppId).not.toHaveBeenCalled()
    dispose()
  })

  // A refused launch looks exactly like an application opening behind this
  // window, and the sidecar's reason is the only thing that tells them apart.
  it('reports a refusal with the sidecar reason', async () => {
    listAppsMock.mockResolvedValue([ZED])
    openInExternalAppMock.mockRejectedValue('launch Zed: exit status 1: bundle is missing')
    const { apps, dispose } = mount()
    await settle()

    apps.launch('zed', '/p')
    await settle()

    expect(showWarnToastMock).toHaveBeenCalledTimes(1)
    expect(showWarnToastMock.mock.calls[0]![0]).toBe(
      'Could not open Zed: launch Zed: exit status 1: bundle is missing',
    )
    dispose()
  })
})

describe('useExternalApps refreshing', () => {
  it('re-pins when the remembered application is no longer detected', async () => {
    listAppsMock.mockResolvedValue([VSCODE, ZED])
    prefs.preferredExternalAppId.mockReturnValue('vscode')
    const { apps, dispose } = mount()
    await settle()

    listAppsMock.mockResolvedValue([ZED])
    await apps.refresh()

    expect(prefs.setPreferredExternalAppId).toHaveBeenCalledWith('zed')
    dispose()
  })

  // Detection can come back empty for a reason that is not "the user
  // uninstalled it" -- a transient probe failure is enough -- and clearing the
  // pin then would throw away a choice that must return when the application
  // does.
  it('keeps the pin when detection comes back empty', async () => {
    listAppsMock.mockResolvedValue([VSCODE])
    prefs.preferredExternalAppId.mockReturnValue('vscode')
    const { apps, dispose } = mount()
    await settle()

    listAppsMock.mockResolvedValue([])
    await apps.refresh()

    expect(prefs.setPreferredExternalAppId).not.toHaveBeenCalled()
    dispose()
  })

  it('leaves a still-detected pin alone', async () => {
    listAppsMock.mockResolvedValue([VSCODE, ZED])
    prefs.preferredExternalAppId.mockReturnValue('zed')
    const { apps, dispose } = mount()
    await settle()

    await apps.refresh()

    expect(prefs.setPreferredExternalAppId).not.toHaveBeenCalled()
    dispose()
  })

  it('reports refreshing while the probe runs, and stops when it lands', async () => {
    listAppsMock.mockResolvedValue([VSCODE])
    const { apps, dispose } = mount()
    await settle()
    expect(apps.refreshing()).toBe(false)

    let settleProbe: (apps: ExternalApp[]) => void = () => {}
    listAppsMock.mockImplementationOnce(
      () => new Promise<ExternalApp[]>((resolve) => { settleProbe = resolve }),
    )
    const done = apps.refresh()
    await settle()
    expect(apps.refreshing()).toBe(true)

    settleProbe([VSCODE])
    await done

    expect(apps.refreshing()).toBe(false)
    dispose()
  })

  it('ignores a second refresh while one is already running', async () => {
    listAppsMock.mockResolvedValue([VSCODE])
    const { apps, dispose } = mount()
    await settle()

    let settleProbe: (apps: ExternalApp[]) => void = () => {}
    listAppsMock.mockImplementationOnce(
      () => new Promise<ExternalApp[]>((resolve) => { settleProbe = resolve }),
    )
    const first = apps.refresh()
    await settle()
    await apps.refresh()

    expect(listAppsMock).toHaveBeenCalledTimes(2)
    settleProbe([VSCODE])
    await first
    dispose()
  })

  it('survives a refresh the sidecar refuses', async () => {
    listAppsMock.mockResolvedValue([VSCODE])
    const { apps, dispose } = mount()
    await settle()

    listAppsMock.mockRejectedValue(new Error('sidecar is gone'))
    await apps.refresh()

    expect(apps.refreshing()).toBe(false)
    dispose()
  })
})

describe('useExternalApps reacting to the pin', () => {
  it('follows a pin that changes after the probe', async () => {
    const [pin, setPin] = createSignal<string | undefined>(undefined)
    prefs.preferredExternalAppId.mockImplementation(() => pin())
    listAppsMock.mockResolvedValue([VSCODE, ZED])
    const { apps, dispose } = mount()
    await settle()
    expect(apps.preferred()).toBeUndefined()

    setPin('zed')

    expect(apps.preferred()?.id).toBe('zed')
    dispose()
  })
})
