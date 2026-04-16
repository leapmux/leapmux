/// <reference types="vitest/globals" />
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'

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

const initialMaximized = { value: false }
vi.mock('~/api/platformBridge', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/api/platformBridge')>()
  return {
    ...actual,
    observeWindowMaximized: (onChange: (max: boolean) => void) => {
      onChange(initialMaximized.value)
      return () => {}
    },
    openWebInspector: vi.fn(),
    quitApp: vi.fn(),
    windowClose: vi.fn(() => Promise.resolve()),
    windowMinimize: vi.fn(() => Promise.resolve()),
    windowToggleMaximize: vi.fn(() => Promise.resolve()),
  }
})

const setShowAboutDialog = vi.fn()
const mockedUserMenuItems = vi.fn((props?: { aboutLabel?: string }) => (
  <>
    <button role="menuitem">Profile...</button>
    <button role="menuitem" onClick={() => setShowAboutDialog(true)}>{props?.aboutLabel ?? 'About...'}</button>
    <button role="menuitem">Preferences...</button>
    <button role="menuitem">Switch mode...</button>
    <button role="menuitem">Log out</button>
  </>
))
vi.mock('~/components/shell/UserMenuItems', () => ({
  UserMenuItems: mockedUserMenuItems,
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

async function renderTitlebar() {
  const { CustomTitlebar } = await import('./CustomTitlebar')
  return render(() => (
    <CustomTitlebar
      onToggleLeftSidebar={() => {}}
      onToggleRightSidebar={() => {}}
      leftSidebarVisible
      rightSidebarVisible
    />
  ))
}

describe('customTitlebar hamburger menu', () => {
  beforeEach(() => {
    initialMaximized.value = false
  })

  it('positions the hamburger menu below the titlebar on Linux', async () => {
    const { getHamburgerPlacement } = await import('./CustomTitlebar')
    expect(getHamburgerPlacement('linux')).toEqual({ placement: 'auto', yOffset: 34 })
  })

  it('renders the hamburger trigger on Linux desktop', async () => {
    await renderTitlebar()
    expect(screen.getByTestId('app-menu-trigger')).toBeInTheDocument()
  })

  it('dropdown exposes merged account and app items', async () => {
    const { container } = await renderTitlebar()
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

  async function clickMenuItem(labelPrefix: string) {
    const { container } = await renderTitlebar()
    const popover = container.querySelector('[data-testid="app-menu"]')!
    const items = Array.from(popover.querySelectorAll<HTMLButtonElement>('button[role="menuitem"]'))
    const match = items.find(el => (el.textContent ?? '').trim().startsWith(labelPrefix))
    if (!match)
      throw new Error(`menuitem not found for "${labelPrefix}"`)
    fireEvent.click(match)
  }

  it('quit invokes quitApp', async () => {
    const { quitApp } = await import('~/api/platformBridge')
    await clickMenuItem('Quit')
    expect(quitApp).toHaveBeenCalledTimes(1)
  })

  it('minimize invokes windowMinimize', async () => {
    const { windowMinimize } = await import('~/api/platformBridge')
    await clickMenuItem('Minimize')
    expect(windowMinimize).toHaveBeenCalledTimes(1)
  })

  it('maximize invokes windowToggleMaximize', async () => {
    const { windowToggleMaximize } = await import('~/api/platformBridge')
    await clickMenuItem('Maximize')
    expect(windowToggleMaximize).toHaveBeenCalledTimes(1)
  })

  it('about… sets the about-dialog signal', async () => {
    setShowAboutDialog.mockClear()
    await clickMenuItem('About LeapMux Desktop')
    expect(setShowAboutDialog).toHaveBeenCalledWith(true)
  })

  it('open Web Inspector invokes openWebInspector', async () => {
    const { openWebInspector } = await import('~/api/platformBridge')
    await clickMenuItem('Open Web Inspector')
    expect(openWebInspector).toHaveBeenCalledTimes(1)
  })

  it('menu item reads "Restore" when the window is maximized', async () => {
    initialMaximized.value = true
    const { container } = await renderTitlebar()
    const popover = container.querySelector('[data-testid="app-menu"]')!
    const items = Array.from(popover.querySelectorAll<HTMLButtonElement>('button[role="menuitem"]'))
    const labels = items.map(el => (el.textContent ?? '').trim())
    expect(labels).toContain('Restore')
    expect(labels).not.toContain('Maximize')
  })

  it('right-side window-control button title switches between Maximize and Restore', async () => {
    initialMaximized.value = true
    await renderTitlebar()
    // The Linux window-controls cluster exposes the maximize/restore button via aria-label.
    expect(screen.getByLabelText('Restore')).toBeInTheDocument()
    expect(screen.queryByLabelText('Maximize')).toBeNull()
  })
})
