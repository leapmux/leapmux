import type { Platform } from './types'

const MAC_MODIFIER_SYMBOLS: Record<string, string> = {
  $mod: '\u2318', // ⌘ Cmd
  Meta: '\u2318',
  Control: '\u2303', // ⌃
  Ctrl: '\u2303',
  Shift: '\u21E7', // ⇧
  Alt: '\u2325', // ⌥ Option
  Option: '\u2325',
}

const STANDARD_MODIFIER_LABELS: Record<string, string> = {
  $mod: 'Ctrl',
  Meta: 'Meta',
  Control: 'Ctrl',
  Ctrl: 'Ctrl',
  Shift: 'Shift',
  Alt: 'Alt',
  Option: 'Alt',
}

const KEY_DISPLAY_NAMES: Record<string, string> = {
  'Escape': 'Esc',
  'Enter': '\u23CE',
  'Backspace': '\u232B',
  'Delete': 'Del',
  'ArrowUp': '\u2191',
  'ArrowDown': '\u2193',
  'ArrowLeft': '\u2190',
  'ArrowRight': '\u2192',
  'Tab': 'Tab',
  ' ': 'Space',
  'Comma': ',',
  'Period': '.',
  'Slash': '/',
  'Backslash': '\\',
  'BracketLeft': '[',
  'BracketRight': ']',
}

/** Mac modifier display order: ⌃ ⌥ ⇧ ⌘ (Apple HIG standard). */
const MAC_MODIFIER_ORDER = ['\u2303', '\u2325', '\u21E7', '\u2318']

/**
 * Format a single chord (e.g. '$mod+Shift+n') for display.
 */
function formatChord(chord: string, platform: Platform): string {
  const parts = chord.split('+')
  const modifiers: string[] = []
  let key = ''

  for (const part of parts) {
    const modLabel = platform === 'mac'
      ? MAC_MODIFIER_SYMBOLS[part]
      : STANDARD_MODIFIER_LABELS[part]
    if (modLabel) {
      modifiers.push(modLabel)
    }
    else {
      key = part
    }
  }

  // Format the key part
  const displayKey = KEY_DISPLAY_NAMES[key] ?? key.toUpperCase()

  if (platform === 'mac') {
    // Sort modifiers to follow Apple HIG order: ⌃ ⌥ ⇧ ⌘
    modifiers.sort((a, b) => MAC_MODIFIER_ORDER.indexOf(a) - MAC_MODIFIER_ORDER.indexOf(b))
    // Mac convention: modifiers run together, then key (no separators)
    return `${modifiers.join('')}${displayKey}`
  }

  // Windows/Linux: Modifier+Modifier+Key with + separators
  return [...modifiers, displayKey].join('+')
}

/**
 * Format a keybinding string for display.
 * Supports chords (space-separated sequences like '$mod+k $mod+s').
 */
export function formatShortcut(key: string, platform: Platform): string {
  const chords = key.split(' ')
  return chords.map(c => formatChord(c, platform)).join(' ')
}
