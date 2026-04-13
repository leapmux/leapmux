import type { Keybinding } from './types'

export const DEFAULT_KEYBINDINGS: readonly Keybinding[] = [
  // --- App-level ---
  { key: '$mod+n', command: 'app.newAgent', when: '!dialogOpen' },
  { key: '$mod+t', command: 'app.newTerminal', when: '!dialogOpen' },
  { key: '$mod+w', command: 'app.closeActiveTab', when: '!dialogOpen' },
  { key: '$mod+Shift+n', command: 'app.newWorkspace', when: '!dialogOpen' },

  // Tab switching by index
  { key: '$mod+1', command: 'app.switchToTab1' },
  { key: '$mod+2', command: 'app.switchToTab2' },
  { key: '$mod+3', command: 'app.switchToTab3' },
  { key: '$mod+4', command: 'app.switchToTab4' },
  { key: '$mod+5', command: 'app.switchToTab5' },
  { key: '$mod+6', command: 'app.switchToTab6' },
  { key: '$mod+7', command: 'app.switchToTab7' },
  { key: '$mod+8', command: 'app.switchToTab8' },
  { key: '$mod+9', command: 'app.switchToTab9' },

  // Tab navigation
  { key: '$mod+BracketLeft', command: 'app.previousTab' },
  { key: '$mod+BracketRight', command: 'app.nextTab' },

  // Layout
  { key: '$mod+Backslash', command: 'app.splitTile' },
  { key: '$mod+b', command: 'app.toggleSidebar' },

  // Preferences
  { key: '$mod+Comma', command: 'app.openPreferences' },

  // Dialog close
  { key: 'Escape', command: 'dialog.close', when: 'dialogOpen' },

  // Desktop-only
  { key: '$mod+q', command: 'app.quit', when: 'isDesktop' },
]
