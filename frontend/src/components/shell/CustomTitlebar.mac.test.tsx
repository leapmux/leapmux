/// <reference types="vitest/globals" />
import type { WindowMode } from '~/api/platformBridge'
import { render } from '@solidjs/testing-library'
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'

import { CustomTitlebar } from './CustomTitlebar'

// The titlebar captures `platform`/`desktop` at module load, so the macOS inset
// path can only be exercised in a file that mocks the platform to 'mac' up
// front (the sibling CustomTitlebar.test.tsx pins it to 'linux').
const { initialMode, modeCb } = vi.hoisted(() => ({
  initialMode: { value: 'normal' as WindowMode },
  // Captures observeWindowMode's callback so tests can drive fullscreen
  // transitions and assert the inset reacts.
  modeCb: { fn: undefined as ((mode: WindowMode) => void) | undefined },
}))

vi.mock('~/lib/shortcuts/platform', () => ({
  getPlatform: () => 'mac',
  isMac: () => true,
}))

vi.mock('~/lib/systemInfo', () => ({
  isDesktopApp: () => true,
}))

vi.mock('~/api/platformBridge', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/api/platformBridge')>()
  return {
    ...actual,
    observeWindowMode: (onChange: (mode: WindowMode) => void) => {
      modeCb.fn = onChange
      onChange(initialMode.value)
      return () => {}
    },
  }
})

vi.mock('~/components/shell/UserMenuItems', () => ({
  AppAboutMenuItem: () => <button role="menuitem">About LeapMux Desktop...</button>,
  UserMenuItems: () => <button role="menuitem">Profile...</button>,
}))
vi.mock('~/components/shell/UserMenuState', () => ({
  setShowAboutDialog: vi.fn(),
}))

// Stub the native Popover API (jsdom doesn't implement it) so DropdownMenu renders.
beforeAll(() => {
  HTMLElement.prototype.showPopover = vi.fn()
  HTMLElement.prototype.hidePopover = vi.fn()
  HTMLElement.prototype.togglePopover = vi.fn(() => true)
})

beforeEach(() => {
  initialMode.value = 'normal'
  modeCb.fn = undefined
})

function renderTitlebar() {
  const result = render(() => <CustomTitlebar variant="minimal" />)
  return result.container.firstElementChild as HTMLElement
}

describe('customTitlebar macOS traffic-light inset', () => {
  it('reserves the 78px inset for the native traffic lights when windowed', () => {
    const titlebar = renderTitlebar()
    expect(titlebar.style.paddingLeft).toBe('78px')
  })

  it('starts without the inset when the window is already fullscreen', () => {
    initialMode.value = 'fullscreen'
    const titlebar = renderTitlebar()
    expect(titlebar.style.paddingLeft).toBe('')
  })

  it('drops the inset on entering fullscreen and restores it on exit', () => {
    const titlebar = renderTitlebar()
    expect(titlebar.style.paddingLeft).toBe('78px')

    // Traffic lights vanish in fullscreen -> the reserved gap must collapse.
    modeCb.fn!('fullscreen')
    expect(titlebar.style.paddingLeft).toBe('')

    modeCb.fn!('normal')
    expect(titlebar.style.paddingLeft).toBe('78px')
  })

  it('keeps the inset while maximized (traffic lights remain)', () => {
    const titlebar = renderTitlebar()
    modeCb.fn!('maximized')
    expect(titlebar.style.paddingLeft).toBe('78px')
  })
})
