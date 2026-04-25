/// <reference types="vitest/globals" />
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'

import { openWebInspector, quitApp, windowMinimize, windowToggleMaximize } from '~/api/platformBridge'
import { CustomTitlebar } from './CustomTitlebar'

// Hoisted so the vi.mock factories below can close over them — vi.mock
// runs above any top-level `const` in the file.
const { initialMaximized, setShowAboutDialog, runtimeLocalSolo, detectedEditors } = vi.hoisted(() => ({
  initialMaximized: { value: false },
  setShowAboutDialog: vi.fn(),
  runtimeLocalSolo: { value: false },
  detectedEditors: { value: [] as Array<{ id: string, displayName: string }> },
}))

vi.mock('~/lib/shortcuts/platform', () => ({
  getPlatform: () => 'linux',
  isMac: () => false,
}))

vi.mock('~/lib/systemInfo', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/lib/systemInfo')>()
  return {
    ...actual,
    isDesktopApp: () => true,
  }
})

vi.mock('~/api/platformBridge', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/api/platformBridge')>()
  return {
    ...actual,
    getRuntimeState: () => Promise.resolve({
      shellMode: runtimeLocalSolo.value ? 'solo' : 'distributed',
      connected: true,
      hubUrl: '',
      capabilities: {
        mode: runtimeLocalSolo.value ? 'tauri-desktop-solo' : 'tauri-desktop-distributed',
        hubTransport: runtimeLocalSolo.value ? 'proxy' : 'direct',
        tunnels: true,
        appControl: true,
        windowControl: true,
        systemPermissions: true,
        localSolo: runtimeLocalSolo.value,
      },
    }),
    observeWindowMaximized: (onChange: (max: boolean) => void) => {
      onChange(initialMaximized.value)
      return () => {}
    },
    openWebInspector: vi.fn(),
    platformBridge: {
      ...actual.platformBridge,
      listEditors: () => Promise.resolve(detectedEditors.value),
      openInEditor: vi.fn(() => Promise.resolve()),
    },
    quitApp: vi.fn(),
    windowClose: vi.fn(() => Promise.resolve()),
    windowMinimize: vi.fn(() => Promise.resolve()),
    windowToggleMaximize: vi.fn(() => Promise.resolve()),
  }
})

vi.mock('~/components/shell/UserMenuItems', () => ({
  UserMenuItems: () => (
    <>
      <button role="menuitem">Profile...</button>
      <button role="menuitem" onClick={() => setShowAboutDialog(true)}>About LeapMux Desktop...</button>
      <button role="menuitem">Preferences...</button>
      <button role="menuitem">Switch mode...</button>
      <button role="menuitem">Log out</button>
    </>
  ),
}))
vi.mock('~/components/shell/UserMenuState', () => ({
  setShowAboutDialog,
}))

// Stub native Popover API (jsdom doesn't implement it).
beforeAll(() => {
  HTMLElement.prototype.showPopover = vi.fn()
  HTMLElement.prototype.hidePopover = vi.fn()
  HTMLElement.prototype.togglePopover = function (this: HTMLElement): boolean {
    // Emulate the toggle event so DropdownMenu's state tracker reacts.
    const event = new Event('toggle')
    Object.defineProperty(event, 'newState', { value: 'open' })
    this.dispatchEvent(event)
    return true
  }
})

function renderTitlebar(activeWorkingDir?: () => string | undefined) {
  return render(() => (
    <CustomTitlebar
      onToggleLeftSidebar={() => {}}
      onToggleRightSidebar={() => {}}
      leftSidebarVisible
      rightSidebarVisible
      activeWorkingDir={activeWorkingDir}
    />
  ))
}

describe('customTitlebar hamburger menu', () => {
  beforeEach(() => {
    initialMaximized.value = false
  })

  it('renders the hamburger trigger on Linux desktop', () => {
    renderTitlebar()
    expect(screen.getByTestId('app-menu-trigger')).toBeInTheDocument()
  })

  it('dropdown exposes merged account and app items', () => {
    const { container } = renderTitlebar()
    const popover = container.querySelector('[data-testid="app-menu"]')
    expect(popover).not.toBeNull()
    const items = popover!.querySelectorAll<HTMLButtonElement>('button[role="menuitem"]')
    const labels = Array.from(items, el => el.textContent?.trim() ?? '')
    expect(labels).toEqual([
      'Profile...',
      'About LeapMux Desktop...',
      'Preferences...',
      'Switch mode...',
      'Log out',
      'Minimize',
      'Maximize',
      'Open Web Inspector',
      expect.stringMatching(/^Quit/),
    ])
  })

  function clickMenuItem(labelPrefix: string) {
    const { container } = renderTitlebar()
    const popover = container.querySelector('[data-testid="app-menu"]')!
    const items = Array.from(popover.querySelectorAll<HTMLButtonElement>('button[role="menuitem"]'))
    const match = items.find(el => (el.textContent ?? '').trim().startsWith(labelPrefix))
    if (!match)
      throw new Error(`menuitem not found for "${labelPrefix}"`)
    fireEvent.click(match)
  }

  it('quit invokes quitApp', () => {
    clickMenuItem('Quit')
    expect(quitApp).toHaveBeenCalledTimes(1)
  })

  it('minimize invokes windowMinimize', () => {
    clickMenuItem('Minimize')
    expect(windowMinimize).toHaveBeenCalledTimes(1)
  })

  it('maximize invokes windowToggleMaximize', () => {
    clickMenuItem('Maximize')
    expect(windowToggleMaximize).toHaveBeenCalledTimes(1)
  })

  it('about… sets the about-dialog signal', () => {
    setShowAboutDialog.mockClear()
    clickMenuItem('About LeapMux Desktop')
    expect(setShowAboutDialog).toHaveBeenCalledWith(true)
  })

  it('open Web Inspector invokes openWebInspector', () => {
    clickMenuItem('Open Web Inspector')
    expect(openWebInspector).toHaveBeenCalledTimes(1)
  })

  it('menu item reads "Restore" when the window is maximized', () => {
    initialMaximized.value = true
    const { container } = renderTitlebar()
    const popover = container.querySelector('[data-testid="app-menu"]')!
    const items = Array.from(popover.querySelectorAll<HTMLButtonElement>('button[role="menuitem"]'))
    const labels = items.map(el => (el.textContent ?? '').trim())
    expect(labels).toContain('Restore')
    expect(labels).not.toContain('Maximize')
  })

  it('right-side window-control button title switches between Maximize and Restore', () => {
    initialMaximized.value = true
    renderTitlebar()
    // The Linux window-controls cluster exposes the maximize/restore button via aria-label.
    expect(screen.getByLabelText('Restore')).toBeInTheDocument()
    expect(screen.queryByLabelText('Maximize')).toBeNull()
  })
})

describe('customTitlebar open-in-editor slot', () => {
  beforeEach(() => {
    runtimeLocalSolo.value = false
    detectedEditors.value = []
    localStorage.clear()
  })

  it('hides the open-in-editor button when not in solo mode (e.g. web)', async () => {
    runtimeLocalSolo.value = false
    detectedEditors.value = [{ id: 'vscode', displayName: 'Visual Studio Code' }]
    const { container } = renderTitlebar(() => '/home/u/proj')
    await new Promise(r => setTimeout(r, 0))
    expect(container.querySelector('[data-testid="open-in-editor"]')).toBeNull()
  })

  it('shows the open-in-editor button before the left-sidebar toggle in solo mode', async () => {
    runtimeLocalSolo.value = true
    detectedEditors.value = [{ id: 'vscode', displayName: 'Visual Studio Code' }]
    const { container } = renderTitlebar(() => '/home/u/proj')
    const button = await screen.findByTestId('open-in-editor')
    const sidebarToggle = container.querySelector('button[aria-label^="Toggle left sidebar"]')
    expect(sidebarToggle).not.toBeNull()
    // Render order: open-in-editor first, then the left-sidebar toggle.
    const cmp = button.compareDocumentPosition(sidebarToggle!)
    expect(cmp & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })
})
