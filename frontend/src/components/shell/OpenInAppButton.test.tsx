import { fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
/// <reference types="vitest/globals" />
import { ExternalAppKind } from '~/generated/proto/leapmux/desktop/v1/frame_pb'

import { KEY_PREFERRED_EXTERNAL_APP, localStorageClearForTests, localStorageGet, localStorageSet } from '~/lib/browserStorage'
import { _resetExternalAppCacheForTests } from '~/lib/externalApps'
import { withPreferences } from '~/test-support/preferencesProvider'
import { OpenInAppButton } from './OpenInAppButton'

// Hoisted so vi.mock factories can access them; vi.mock runs above top-level const.
const { listAppsMock, openInExternalAppMock, runtimeStateMock, showWarnToastMock } = vi.hoisted(() => ({
  listAppsMock: vi.fn(),
  openInExternalAppMock: vi.fn(),
  runtimeStateMock: vi.fn(),
  showWarnToastMock: vi.fn(),
}))

vi.mock('~/api/platformBridge', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/api/platformBridge')>()
  return {
    ...actual,
    getRuntimeState: () => runtimeStateMock(),
    platformBridge: {
      ...actual.platformBridge,
      listExternalApps: (refresh?: boolean) => listAppsMock(refresh ?? false),
      openInExternalApp: (...args: unknown[]) => openInExternalAppMock(...args),
    },
  }
})

// Spread the original: the module has other exports, and the graph under test
// reaches them through modules this file never names.
vi.mock('~/components/common/Toast', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/components/common/Toast')>()
  return { ...actual, showWarnToast: (...args: unknown[]) => showWarnToastMock(...args) }
})

function soloRuntimeState(localSolo: boolean) {
  return {
    shellMode: localSolo ? 'solo' : 'distributed',
    connected: true,
    hubUrl: '',
    capabilities: {
      mode: localSolo ? 'tauri-desktop-solo' : 'tauri-desktop-distributed',
      hubTransport: localSolo ? 'proxy' : 'direct',
      tunnels: true,
      appControl: true,
      windowControl: true,
      systemPermissions: true,
      localSolo,
    },
  }
}

beforeAll(() => {
  // jsdom doesn't implement the Popover API.
  HTMLElement.prototype.showPopover = vi.fn()
  HTMLElement.prototype.hidePopover = vi.fn()
})

beforeEach(() => {
  localStorageClearForTests()
  listAppsMock.mockReset()
  openInExternalAppMock.mockReset()
  runtimeStateMock.mockReset()
  runtimeStateMock.mockResolvedValue(soloRuntimeState(true))
  openInExternalAppMock.mockResolvedValue(undefined)
  showWarnToastMock.mockReset()
  _resetExternalAppCacheForTests()
})

afterEach(() => {
  _resetExternalAppCacheForTests()
})

function renderButton(workingDir: string | undefined = '/home/u/proj') {
  return render(withPreferences(() => <OpenInAppButton workingDir={() => workingDir} />))
}

function renderButtonNoDir() {
  return render(withPreferences(() => <OpenInAppButton workingDir={() => undefined} />))
}

describe('open in app button', () => {
  it('renders nothing when workingDir is empty', async () => {
    listAppsMock.mockResolvedValue([{ id: 'vscode', displayName: 'VS Code', kind: ExternalAppKind.EDITOR }])
    const { container } = renderButtonNoDir()
    // Wait a tick for the resource to settle; nothing should render.
    await new Promise(r => setTimeout(r, 0))
    expect(container.querySelector('[data-testid="open-in-app"]')).toBeNull()
  })

  it('renders nothing when not in solo Tauri', async () => {
    runtimeStateMock.mockResolvedValue(soloRuntimeState(false))
    listAppsMock.mockResolvedValue([{ id: 'vscode', displayName: 'VS Code', kind: ExternalAppKind.EDITOR }])
    const { container } = renderButton()
    await new Promise(r => setTimeout(r, 0))
    expect(container.querySelector('[data-testid="open-in-app"]')).toBeNull()
  })

  it('renders nothing when no applications are detected', async () => {
    listAppsMock.mockResolvedValue([])
    const { container } = renderButton()
    await new Promise(r => setTimeout(r, 0))
    expect(container.querySelector('[data-testid="open-in-app"]')).toBeNull()
  })

  it('shows "Open in …" when no MRU is set', async () => {
    listAppsMock.mockResolvedValue([{ id: 'vscode', displayName: 'VS Code', kind: ExternalAppKind.EDITOR }])
    renderButton()
    const main = await screen.findByTestId('open-in-app-main')
    expect(main.textContent).toContain('Open in …')
  })

  it('clicking main with no MRU does NOT launch (opens menu instead)', async () => {
    listAppsMock.mockResolvedValue([{ id: 'vscode', displayName: 'VS Code', kind: ExternalAppKind.EDITOR }])
    renderButton()
    const main = await screen.findByTestId('open-in-app-main')
    fireEvent.click(main)
    expect(openInExternalAppMock).not.toHaveBeenCalled()
  })

  it('shows "Open in <name>" when MRU is set and matches a detected application', async () => {
    listAppsMock.mockResolvedValue([
      { id: 'vscode', displayName: 'Visual Studio Code', kind: ExternalAppKind.EDITOR },
      { id: 'zed', displayName: 'Zed', kind: ExternalAppKind.EDITOR },
    ])
    localStorageSet(KEY_PREFERRED_EXTERNAL_APP, 'zed')
    renderButton()
    const main = await screen.findByTestId('open-in-app-main')
    await waitFor(() => expect(main.textContent).toContain('Open in Zed'))
  })

  it('clicking main with MRU set launches that application', async () => {
    listAppsMock.mockResolvedValue([
      { id: 'vscode', displayName: 'Visual Studio Code', kind: ExternalAppKind.EDITOR },
    ])
    localStorageSet(KEY_PREFERRED_EXTERNAL_APP, 'vscode')
    renderButton('/home/u/proj')
    const main = await screen.findByTestId('open-in-app-main')
    await waitFor(() => expect(main.textContent).toContain('Open in Visual Studio Code'))
    fireEvent.click(main)
    expect(openInExternalAppMock).toHaveBeenCalledWith('vscode', '/home/u/proj')
  })

  // Picking a row both LAUNCHES and remembers, on every surface. It used to
  // only remember, which left a menu that looks like a launcher doing nothing
  // when clicked -- and made this list mean two different things depending on
  // which menu it was rendered in.
  it('picking a menu row launches that application and remembers it', async () => {
    listAppsMock.mockResolvedValue([
      { id: 'vscode', displayName: 'Visual Studio Code', kind: ExternalAppKind.EDITOR },
      { id: 'zed', displayName: 'Zed', kind: ExternalAppKind.EDITOR },
    ])
    renderButton('/p')
    const item = await screen.findByTestId('open-in-app-item-zed')
    fireEvent.click(item)
    expect(openInExternalAppMock).toHaveBeenCalledWith('zed', '/p')
    expect(localStorageGet<string>(KEY_PREFERRED_EXTERNAL_APP)).toBe('zed')
    // …and the face now names it, so the next launch needs no menu at all.
    const main = await screen.findByTestId('open-in-app-main')
    await waitFor(() => expect(main.textContent).toContain('Open in Zed'))
  })

  // A launch that the sidecar refuses looks exactly like an application
  // opening behind this window, so it is REPORTED rather than only logged.
  // The reporting lives in `useExternalApps.launch`, which every surface that
  // offers this list shares -- this is the one test that exercises it.
  it('reports a refused launch, naming the application', async () => {
    listAppsMock.mockResolvedValue([
      { id: 'zed', displayName: 'Zed', kind: ExternalAppKind.EDITOR },
    ])
    const refused = new Error('launch Zed: no such file or directory')
    openInExternalAppMock.mockRejectedValue(refused)
    renderButton('/p')

    const item = await screen.findByTestId('open-in-app-item-zed')
    fireEvent.click(item)

    await waitFor(() => expect(showWarnToastMock).toHaveBeenCalledTimes(1))
    expect(showWarnToastMock.mock.calls[0]![0]).toContain('Zed')
    expect(showWarnToastMock.mock.calls[0]![1]).toBe(refused)
    // The pick is still remembered: the application exists, and a failure to
    // launch it once is no reason to make the user choose again.
    expect(localStorageGet<string>(KEY_PREFERRED_EXTERNAL_APP)).toBe('zed')
  })

  it('does not launch a pick while the working directory is unknown', async () => {
    listAppsMock.mockResolvedValue([
      { id: 'zed', displayName: 'Zed', kind: ExternalAppKind.EDITOR },
    ])
    render(withPreferences(() => <OpenInAppButton workingDir={() => ''} />))
    await new Promise(r => setTimeout(r, 0))
    expect(screen.queryByTestId('open-in-app')).toBeNull()
    expect(openInExternalAppMock).not.toHaveBeenCalled()
  })

  // The file manager is a different kind of target from an editor, so it keeps
  // its own group ahead of them rather than sorting in among them by name --
  // "Finder" would otherwise file between "Cursor" and "Visual Studio Code".
  it('lists the file manager first, ahead of the editors', async () => {
    listAppsMock.mockResolvedValue([
      { id: 'vscode', displayName: 'Visual Studio Code', kind: ExternalAppKind.EDITOR },
      { id: 'file-manager', displayName: 'Finder', kind: ExternalAppKind.FILE_MANAGER },
      { id: 'cursor', displayName: 'Cursor', kind: ExternalAppKind.EDITOR },
    ])
    const { container } = renderButton()
    await screen.findByTestId('open-in-app-item-file-manager')
    const labels = Array.from(
      container.querySelectorAll<HTMLElement>('[data-testid^="open-in-app-item-"] > span > span'),
    ).map(el => el.textContent?.trim())
    expect(labels).toEqual(['Finder', 'Cursor', 'Visual Studio Code'])
  })

  it('can remember the file manager, and names it on the face', async () => {
    listAppsMock.mockResolvedValue([
      { id: 'file-manager', displayName: 'Finder', kind: ExternalAppKind.FILE_MANAGER },
      { id: 'vscode', displayName: 'Visual Studio Code', kind: ExternalAppKind.EDITOR },
    ])
    renderButton('/p')
    fireEvent.click(await screen.findByTestId('open-in-app-item-file-manager'))
    expect(openInExternalAppMock).toHaveBeenCalledWith('file-manager', '/p')
    const main = await screen.findByTestId('open-in-app-main')
    await waitFor(() => expect(main.textContent).toContain('Open in Finder'))
  })

  it('falls back to "Open in …" when MRU points at an application that is no longer detected', async () => {
    listAppsMock.mockResolvedValue([{ id: 'vscode', displayName: 'VS Code', kind: ExternalAppKind.EDITOR }])
    localStorageSet(KEY_PREFERRED_EXTERNAL_APP, 'zed')
    renderButton()
    const main = await screen.findByTestId('open-in-app-main')
    await waitFor(() => expect(main.textContent).toContain('Open in …'))
  })

  it('chevron button has aria-haspopup=menu', async () => {
    listAppsMock.mockResolvedValue([{ id: 'vscode', displayName: 'VS Code', kind: ExternalAppKind.EDITOR }])
    renderButton()
    const chevron = await screen.findByTestId('open-in-app-chevron')
    expect(chevron.getAttribute('aria-haspopup')).toBe('menu')
  })

  it('sorts the editors alphabetically by display name', async () => {
    listAppsMock.mockResolvedValue([
      { id: 'zed', displayName: 'Zed', kind: ExternalAppKind.EDITOR },
      { id: 'intellij-idea-ultimate', displayName: 'IntelliJ IDEA Ultimate', kind: ExternalAppKind.EDITOR },
      { id: 'vscode', displayName: 'Visual Studio Code', kind: ExternalAppKind.EDITOR },
    ])
    const { container } = renderButton()
    await screen.findByTestId('open-in-app-item-vscode')
    const labels = Array.from(
      container.querySelectorAll<HTMLElement>('[data-testid^="open-in-app-item-"] > span > span'),
    ).map(el => el.textContent?.trim())
    expect(labels).toEqual([
      'IntelliJ IDEA Ultimate',
      'Visual Studio Code',
      'Zed',
    ])
  })

  describe('refresh app list', () => {
    it('asks the bridge to re-probe and updates the menu', async () => {
      listAppsMock
        .mockResolvedValueOnce([{ id: 'vscode', displayName: 'VS Code', kind: ExternalAppKind.EDITOR }])
        .mockResolvedValueOnce([
          { id: 'vscode', displayName: 'VS Code', kind: ExternalAppKind.EDITOR },
          { id: 'zed', displayName: 'Zed', kind: ExternalAppKind.EDITOR },
        ])
      renderButton()
      const refreshBtn = await screen.findByTestId('open-in-app-refresh')
      fireEvent.click(refreshBtn)
      await waitFor(() => {
        expect(listAppsMock).toHaveBeenCalledWith(true)
      })
      await screen.findByTestId('open-in-app-item-zed')
    })

    it('migrates MRU to the first remaining application when the prior one disappears', async () => {
      listAppsMock
        .mockResolvedValueOnce([
          { id: 'vscode', displayName: 'VS Code', kind: ExternalAppKind.EDITOR },
          { id: 'zed', displayName: 'Zed', kind: ExternalAppKind.EDITOR },
        ])
        .mockResolvedValueOnce([{ id: 'vscode', displayName: 'VS Code', kind: ExternalAppKind.EDITOR }])
      localStorageSet(KEY_PREFERRED_EXTERNAL_APP, 'zed')
      renderButton('/p')
      // MRU is `zed` initially.
      const main = await screen.findByTestId('open-in-app-main')
      await waitFor(() => expect(main.textContent).toContain('Open in Zed'))

      const refreshBtn = await screen.findByTestId('open-in-app-refresh')
      fireEvent.click(refreshBtn)
      // After refresh, Zed is gone; MRU migrates to VS Code.
      await waitFor(() => expect(main.textContent).toContain('Open in VS Code'))
      expect(localStorageGet<string>(KEY_PREFERRED_EXTERNAL_APP)).toBe('vscode')
    })

    // A refresh that comes back EMPTY changes nothing. Detection can return
    // an empty list for a reason that is not "the user uninstalled it" — a
    // transient probe failure is enough — so the pin must survive and come
    // back with the application.
    //
    // This test used to assert the same outcome without ever reaching the
    // branch that decides it: it passed against a version that DELETED the
    // pin. It now waits for the button to prove the MRU is live before
    // refreshing, so the empty-list path is genuinely exercised.
    it('leaves the MRU alone when refresh returns no applications', async () => {
      listAppsMock
        .mockResolvedValueOnce([{ id: 'vscode', displayName: 'VS Code', kind: ExternalAppKind.EDITOR }])
        .mockResolvedValueOnce([])
      localStorageSet(KEY_PREFERRED_EXTERNAL_APP, 'vscode')
      renderButton('/p')

      // The pin is live in the reactive signal, not merely in storage —
      // otherwise the branch under test is never entered.
      const main = await screen.findByTestId('open-in-app-main')
      await waitFor(() => expect(main.textContent).toContain('Open in VS Code'))

      const refreshBtn = await screen.findByTestId('open-in-app-refresh')
      fireEvent.click(refreshBtn)
      await waitFor(() => expect(listAppsMock).toHaveBeenCalledTimes(2))
      await waitFor(() => expect(screen.queryByTestId('open-in-app')).toBeNull())

      // Both tiers keep the choice: storage, so it survives a reload, and
      // the signal, so the settings row still shows what is pinned.
      expect(localStorageGet<string>(KEY_PREFERRED_EXTERNAL_APP)).toBe('vscode')
    })

    it('disables the chevron and shows a spinner there while the refresh is in flight', async () => {
      let resolveRefresh: (v: { id: string, displayName: string, kind: ExternalAppKind }[]) => void = () => {}
      listAppsMock
        .mockResolvedValueOnce([{ id: 'vscode', displayName: 'VS Code', kind: ExternalAppKind.EDITOR }])
        .mockImplementationOnce(() => new Promise((r) => {
          resolveRefresh = r
        }))
      renderButton()
      const refreshBtn = await screen.findByTestId('open-in-app-refresh')
      const chevron = await screen.findByTestId('open-in-app-chevron') as HTMLButtonElement
      expect(chevron.disabled).toBe(false)
      fireEvent.click(refreshBtn)
      await waitFor(() => expect(chevron.disabled).toBe(true))
      // Resolve the in-flight refresh; the chevron flips back to enabled.
      resolveRefresh([{ id: 'vscode', displayName: 'VS Code', kind: ExternalAppKind.EDITOR }])
      await waitFor(() => expect(chevron.disabled).toBe(false))
    })
  })
})
