import type { Keybinding } from './types'

export const DEFAULT_KEYBINDINGS: readonly Keybinding[] = [
  // --- App-level ---
  { key: '$mod+n', command: 'app.newAgent', when: '!dialogOpen' },
  { key: '$mod+t', command: 'app.newTerminal', when: '!dialogOpen' },
  { key: '$mod+w', command: 'app.closeActiveTab', when: '!dialogOpen' },
  { key: '$mod+Shift+n', command: 'app.newAgentDialog', when: '!dialogOpen' },
  { key: '$mod+Shift+t', command: 'app.newTerminalDialog', when: '!dialogOpen' },
  { key: '$mod+Alt+n', command: 'app.newWorkspaceDialog', when: '!dialogOpen' },
  { key: '$mod+Shift+o', command: 'app.toggleFloatingTab', when: '!dialogOpen' },
  { key: '$mod+r', command: 'app.refreshDirectoryTree' },
  { key: 'F5', command: 'app.refreshDirectoryTree' },
  { key: '$mod+Shift+h', command: 'app.toggleHiddenFiles' },

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
  { key: '$mod+PageUp', command: 'app.previousTab' },
  { key: '$mod+PageDown', command: 'app.nextTab' },

  // Page scrolling
  { key: 'Alt+PageUp', command: 'app.scrollActiveTabPageUp', when: '!dialogOpen' },
  { key: 'Alt+PageDown', command: 'app.scrollActiveTabPageDown', when: '!dialogOpen' },

  // Layout
  { key: '$mod+Backslash', command: 'app.splitTileHorizontal' },
  { key: '$mod+Shift+Backslash', command: 'app.splitTileVertical' },
  { key: '$mod+Shift+BracketLeft', command: 'app.toggleLeftSidebar' },
  { key: '$mod+Shift+BracketRight', command: 'app.toggleRightSidebar' },

  // Preferences
  { key: '$mod+Comma', command: 'app.openPreferences' },
  { key: '$mod+Alt+i', command: 'app.openWebInspector', when: 'isDesktop' },
  { key: 'F12', command: 'app.openWebInspector', when: 'isDesktop' },
  { key: '$mod+-', command: 'app.zoomOutWebview', when: 'isDesktop' },
  { key: '$mod+NumpadSubtract', command: 'app.zoomOutWebview', when: 'isDesktop' },
  { key: '$mod+=', command: 'app.zoomInWebview', when: 'isDesktop' },
  { key: '$mod+NumpadAdd', command: 'app.zoomInWebview', when: 'isDesktop' },
  { key: '$mod+0', command: 'app.resetWebviewZoom', when: 'isDesktop' },
  { key: '$mod+Numpad0', command: 'app.resetWebviewZoom', when: 'isDesktop' },

  // Dialog close
  { key: 'Escape', command: 'dialog.close', when: 'dialogOpen' },

  // Terminal cursor navigation (macOS)
  { key: '$mod+ArrowLeft', command: 'terminal.lineStart', when: 'terminalFocused && platform == "mac"' },
  { key: '$mod+ArrowRight', command: 'terminal.lineEnd', when: 'terminalFocused && platform == "mac"' },
  { key: 'Alt+ArrowLeft', command: 'terminal.wordLeft', when: 'terminalFocused && platform == "mac"' },
  { key: 'Alt+ArrowRight', command: 'terminal.wordRight', when: 'terminalFocused && platform == "mac"' },

  // Desktop-only
  { key: '$mod+q', command: 'app.quit', when: 'isDesktop' },
]
