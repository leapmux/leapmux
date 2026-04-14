import { describe, expect, it } from 'vitest'

import { formatShortcut } from './display'

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
  })

  describe('plain keys', () => {
    it('formats single character keys', () => {
      expect(formatShortcut('?', 'mac')).toBe('?')
    })

    it('formats Escape', () => {
      expect(formatShortcut('Escape', 'linux')).toBe('Esc')
    })
  })
})
