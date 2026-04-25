/// <reference types="vitest/globals" />
import { fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'

import { _resetEditorCacheForTests } from '~/lib/externalEditors'
import { OpenInEditorButton } from './OpenInEditorButton'

// Hoisted so vi.mock factories can access them; vi.mock runs above top-level const.
const { listEditorsMock, openInEditorMock, runtimeStateMock } = vi.hoisted(() => ({
  listEditorsMock: vi.fn(),
  openInEditorMock: vi.fn(),
  runtimeStateMock: vi.fn(),
}))

vi.mock('~/api/platformBridge', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/api/platformBridge')>()
  return {
    ...actual,
    getRuntimeState: () => runtimeStateMock(),
    platformBridge: {
      ...actual.platformBridge,
      listEditors: (refresh?: boolean) => listEditorsMock(refresh ?? false),
      openInEditor: (...args: unknown[]) => openInEditorMock(...args),
    },
  }
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
  localStorage.clear()
  listEditorsMock.mockReset()
  openInEditorMock.mockReset()
  runtimeStateMock.mockReset()
  runtimeStateMock.mockResolvedValue(soloRuntimeState(true))
  openInEditorMock.mockResolvedValue(undefined)
  _resetEditorCacheForTests()
})

afterEach(() => {
  _resetEditorCacheForTests()
})

function renderButton(workingDir: string | undefined = '/home/u/proj') {
  return render(() => <OpenInEditorButton workingDir={() => workingDir} />)
}

function renderButtonNoDir() {
  return render(() => <OpenInEditorButton workingDir={() => undefined} />)
}

describe('open in editor button', () => {
  it('renders nothing when workingDir is empty', async () => {
    listEditorsMock.mockResolvedValue([{ id: 'vscode', displayName: 'VS Code' }])
    const { container } = renderButtonNoDir()
    // Wait a tick for the resource to settle; nothing should render.
    await new Promise(r => setTimeout(r, 0))
    expect(container.querySelector('[data-testid="open-in-editor"]')).toBeNull()
  })

  it('renders nothing when not in solo Tauri', async () => {
    runtimeStateMock.mockResolvedValue(soloRuntimeState(false))
    listEditorsMock.mockResolvedValue([{ id: 'vscode', displayName: 'VS Code' }])
    const { container } = renderButton()
    await new Promise(r => setTimeout(r, 0))
    expect(container.querySelector('[data-testid="open-in-editor"]')).toBeNull()
  })

  it('renders nothing when no editors are detected', async () => {
    listEditorsMock.mockResolvedValue([])
    const { container } = renderButton()
    await new Promise(r => setTimeout(r, 0))
    expect(container.querySelector('[data-testid="open-in-editor"]')).toBeNull()
  })

  it('shows "Open in …" when no MRU is set', async () => {
    listEditorsMock.mockResolvedValue([{ id: 'vscode', displayName: 'VS Code' }])
    renderButton()
    const main = await screen.findByTestId('open-in-editor-main')
    expect(main.textContent).toContain('Open in …')
  })

  it('clicking main with no MRU does NOT launch (opens menu instead)', async () => {
    listEditorsMock.mockResolvedValue([{ id: 'vscode', displayName: 'VS Code' }])
    renderButton()
    const main = await screen.findByTestId('open-in-editor-main')
    fireEvent.click(main)
    expect(openInEditorMock).not.toHaveBeenCalled()
  })

  it('shows "Open in <name>" when MRU is set and matches a detected editor', async () => {
    listEditorsMock.mockResolvedValue([
      { id: 'vscode', displayName: 'Visual Studio Code' },
      { id: 'zed', displayName: 'Zed' },
    ])
    localStorage.setItem('leapmux:preferred-editor', JSON.stringify('zed'))
    renderButton()
    const main = await screen.findByTestId('open-in-editor-main')
    await waitFor(() => expect(main.textContent).toContain('Open in Zed'))
  })

  it('clicking main with MRU set launches that editor', async () => {
    listEditorsMock.mockResolvedValue([
      { id: 'vscode', displayName: 'Visual Studio Code' },
    ])
    localStorage.setItem('leapmux:preferred-editor', JSON.stringify('vscode'))
    renderButton('/home/u/proj')
    const main = await screen.findByTestId('open-in-editor-main')
    await waitFor(() => expect(main.textContent).toContain('Open in Visual Studio Code'))
    fireEvent.click(main)
    expect(openInEditorMock).toHaveBeenCalledWith('vscode', '/home/u/proj')
  })

  it('selecting a dropdown item only updates the MRU; it does NOT launch', async () => {
    listEditorsMock.mockResolvedValue([
      { id: 'vscode', displayName: 'Visual Studio Code' },
      { id: 'zed', displayName: 'Zed' },
    ])
    renderButton('/p')
    const item = await screen.findByTestId('open-in-editor-item-zed')
    fireEvent.click(item)
    expect(openInEditorMock).not.toHaveBeenCalled()
    expect(localStorage.getItem('leapmux:preferred-editor')).toBe(JSON.stringify('zed'))
    // …and the next click on the main face uses the freshly chosen editor.
    const main = await screen.findByTestId('open-in-editor-main')
    await waitFor(() => expect(main.textContent).toContain('Open in Zed'))
    fireEvent.click(main)
    expect(openInEditorMock).toHaveBeenCalledWith('zed', '/p')
  })

  it('falls back to "Open in …" when MRU points at an editor that is no longer detected', async () => {
    listEditorsMock.mockResolvedValue([{ id: 'vscode', displayName: 'VS Code' }])
    localStorage.setItem('leapmux:preferred-editor', JSON.stringify('zed'))
    renderButton()
    const main = await screen.findByTestId('open-in-editor-main')
    await waitFor(() => expect(main.textContent).toContain('Open in …'))
  })

  it('chevron button has aria-haspopup=menu', async () => {
    listEditorsMock.mockResolvedValue([{ id: 'vscode', displayName: 'VS Code' }])
    renderButton()
    const chevron = await screen.findByTestId('open-in-editor-chevron')
    expect(chevron.getAttribute('aria-haspopup')).toBe('menu')
  })

  it('renders detected editors sorted alphabetically by display name', async () => {
    listEditorsMock.mockResolvedValue([
      { id: 'zed', displayName: 'Zed' },
      { id: 'intellij-idea-ultimate', displayName: 'IntelliJ IDEA Ultimate' },
      { id: 'vscode', displayName: 'Visual Studio Code' },
    ])
    const { container } = renderButton()
    await screen.findByTestId('open-in-editor-item-vscode')
    const labels = Array.from(
      container.querySelectorAll<HTMLElement>('[data-testid^="open-in-editor-item-"] > span > span'),
    ).map(el => el.textContent?.trim())
    expect(labels).toEqual([
      'IntelliJ IDEA Ultimate',
      'Visual Studio Code',
      'Zed',
    ])
  })

  describe('refresh editor list', () => {
    it('asks the bridge to re-probe and updates the menu', async () => {
      listEditorsMock
        .mockResolvedValueOnce([{ id: 'vscode', displayName: 'VS Code' }])
        .mockResolvedValueOnce([
          { id: 'vscode', displayName: 'VS Code' },
          { id: 'zed', displayName: 'Zed' },
        ])
      renderButton()
      const refreshBtn = await screen.findByTestId('open-in-editor-refresh')
      fireEvent.click(refreshBtn)
      await waitFor(() => {
        expect(listEditorsMock).toHaveBeenCalledWith(true)
      })
      await screen.findByTestId('open-in-editor-item-zed')
    })

    it('migrates MRU to the first remaining editor when the prior MRU disappears', async () => {
      listEditorsMock
        .mockResolvedValueOnce([
          { id: 'vscode', displayName: 'VS Code' },
          { id: 'zed', displayName: 'Zed' },
        ])
        .mockResolvedValueOnce([{ id: 'vscode', displayName: 'VS Code' }])
      localStorage.setItem('leapmux:preferred-editor', JSON.stringify('zed'))
      renderButton('/p')
      // MRU is `zed` initially.
      const main = await screen.findByTestId('open-in-editor-main')
      await waitFor(() => expect(main.textContent).toContain('Open in Zed'))

      const refreshBtn = await screen.findByTestId('open-in-editor-refresh')
      fireEvent.click(refreshBtn)
      // After refresh, Zed is gone; MRU migrates to VS Code.
      await waitFor(() => expect(main.textContent).toContain('Open in VS Code'))
      expect(localStorage.getItem('leapmux:preferred-editor')).toBe(JSON.stringify('vscode'))
    })

    it('clears in-memory MRU when refresh returns no editors but leaves storage alone', async () => {
      listEditorsMock
        .mockResolvedValueOnce([{ id: 'vscode', displayName: 'VS Code' }])
        .mockResolvedValueOnce([])
      localStorage.setItem('leapmux:preferred-editor', JSON.stringify('vscode'))
      renderButton('/p')
      const refreshBtn = await screen.findByTestId('open-in-editor-refresh')
      fireEvent.click(refreshBtn)
      await waitFor(() => expect(listEditorsMock).toHaveBeenCalledTimes(2))
      // localStorage MRU is preserved so the user's choice returns when they
      // re-install the editor — only the in-memory signal is cleared.
      expect(localStorage.getItem('leapmux:preferred-editor')).toBe(JSON.stringify('vscode'))
    })

    it('disables the chevron and shows a spinner there while the refresh is in flight', async () => {
      let resolveRefresh: (v: { id: string, displayName: string }[]) => void = () => {}
      listEditorsMock
        .mockResolvedValueOnce([{ id: 'vscode', displayName: 'VS Code' }])
        .mockImplementationOnce(() => new Promise((r) => {
          resolveRefresh = r
        }))
      renderButton()
      const refreshBtn = await screen.findByTestId('open-in-editor-refresh')
      const chevron = await screen.findByTestId('open-in-editor-chevron') as HTMLButtonElement
      expect(chevron.disabled).toBe(false)
      fireEvent.click(refreshBtn)
      await waitFor(() => expect(chevron.disabled).toBe(true))
      // Resolve the in-flight refresh; the chevron flips back to enabled.
      resolveRefresh([{ id: 'vscode', displayName: 'VS Code' }])
      await waitFor(() => expect(chevron.disabled).toBe(false))
    })
  })
})
