import { beforeEach, describe, expect, it, vi } from 'vitest'

const { getBindingForCommand, getBindingsForCommand } = vi.hoisted(() => ({
  getBindingForCommand: vi.fn(),
  getBindingsForCommand: vi.fn((): string[] => []),
}))

vi.mock('./keybindings', () => ({
  getBindingForCommand,
  getBindingsForCommand,
}))

import { formatShortcut, getShortcutHint, getShortcutHintsText } from './display'

beforeEach(() => {
  getBindingForCommand.mockReset()
  getBindingsForCommand.mockReset()
  getBindingsForCommand.mockReturnValue([])
})

describe('formatShortcut', () => {
  describe('mac platform', () => {
    it('formats $mod as ⌘', () => {
      expect(formatShortcut('$mod+n', 'mac')).toBe('\u2318N')
    })

    it('formats $mod+Shift', () => {
      expect(formatShortcut('$mod+Shift+n', 'mac')).toBe('\u21E7\u2318N')
    })

    it('formats Control', () => {
      expect(formatShortcut('Control+q', 'mac')).toBe('\u2303Q')
    })

    it('formats Alt as ⌥', () => {
      expect(formatShortcut('Alt+n', 'mac')).toBe('\u2325N')
    })

    it('orders Alt before Cmd in combined shortcuts', () => {
      expect(formatShortcut('$mod+Alt+n', 'mac')).toBe('\u2325\u2318N')
    })

    it('formats Escape', () => {
      expect(formatShortcut('Escape', 'mac')).toBe('Esc')
    })

    it('formats chord sequences', () => {
      expect(formatShortcut('$mod+k $mod+s', 'mac')).toBe('\u2318K \u2318S')
    })

    it('formats special keys', () => {
      expect(formatShortcut('$mod+Comma', 'mac')).toBe('\u2318,')
      expect(formatShortcut('$mod+Backslash', 'mac')).toBe('\u2318\\')
      expect(formatShortcut('$mod+BracketLeft', 'mac')).toBe('\u2318[')
      expect(formatShortcut('$mod+BracketRight', 'mac')).toBe('\u2318]')
      expect(formatShortcut('$mod+PageUp', 'mac')).toBe('\u2318PageUp')
      expect(formatShortcut('Alt+PageDown', 'mac')).toBe('\u2325PageDown')
    })

    it('formats keypad keys', () => {
      expect(formatShortcut('$mod+NumpadAdd', 'mac')).toBe('\u2318Num+')
      expect(formatShortcut('$mod+NumpadSubtract', 'mac')).toBe('\u2318Num-')
      expect(formatShortcut('$mod+Numpad0', 'mac')).toBe('\u2318Num0')
    })

    it('formats number keys', () => {
      expect(formatShortcut('$mod+1', 'mac')).toBe('\u23181')
    })
  })

  describe('windows platform', () => {
    it('formats $mod as Ctrl', () => {
      expect(formatShortcut('$mod+n', 'windows')).toBe('Ctrl+N')
    })

    it('formats $mod+Shift', () => {
      expect(formatShortcut('$mod+Shift+n', 'windows')).toBe('Ctrl+Shift+N')
    })

    it('formats chord sequences', () => {
      expect(formatShortcut('$mod+k $mod+s', 'windows')).toBe('Ctrl+K Ctrl+S')
    })

    it('formats Control', () => {
      expect(formatShortcut('Control+q', 'windows')).toBe('Ctrl+Q')
    })
  })

  describe('linux platform', () => {
    it('formats $mod as Ctrl', () => {
      expect(formatShortcut('$mod+n', 'linux')).toBe('Ctrl+N')
    })

    it('formats $mod+Shift', () => {
      expect(formatShortcut('$mod+Shift+n', 'linux')).toBe('Ctrl+Shift+N')
    })

    it('formats Control+q', () => {
      expect(formatShortcut('Control+q', 'linux')).toBe('Ctrl+Q')
    })

    it('formats page navigation keys', () => {
      expect(formatShortcut('$mod+PageUp', 'linux')).toBe('Ctrl+PageUp')
      expect(formatShortcut('Alt+PageDown', 'linux')).toBe('Alt+PageDown')
    })

    it('formats keypad keys', () => {
      expect(formatShortcut('$mod+NumpadAdd', 'linux')).toBe('Ctrl+Num+')
      expect(formatShortcut('$mod+NumpadSubtract', 'linux')).toBe('Ctrl+Num-')
      expect(formatShortcut('$mod+Numpad0', 'linux')).toBe('Ctrl+Num0')
    })
  })

  describe('plain keys', () => {
    it('formats single character keys', () => {
      expect(formatShortcut('?', 'mac')).toBe('?')
    })

    it('formats Escape', () => {
      expect(formatShortcut('Escape', 'linux')).toBe('Esc')
    })
  })

  describe('command hint helpers', () => {
    it('returns the primary shortcut hint for a command', () => {
      getBindingForCommand.mockReturnValue('$mod+Comma')
      expect(getShortcutHint('app.openPreferences')).toBe('Ctrl+,')
    })

    it('joins multiple shortcut hints for a command', () => {
      getBindingsForCommand.mockReturnValue(['$mod+Alt+i', 'F12'])
      expect(getShortcutHintsText('app.openWebInspector')).toBe('Ctrl+Alt+I / F12')
    })
  })
})
