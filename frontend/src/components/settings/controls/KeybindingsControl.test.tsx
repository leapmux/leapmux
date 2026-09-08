import type { UserKeybindingOverride } from '~/lib/shortcuts/types'
import { cleanup, fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { PreferencesProvider } from '~/context/PreferencesContext'
import { registerCommand, resetCommands } from '~/lib/shortcuts/commands'
import { WORKSPACE_KEYBINDINGS } from '~/lib/shortcuts/defaults'
import { getPlatform } from '~/lib/shortcuts/platform'

import { buildCommandRows, chordFromEvent, KeybindingsControl } from './KeybindingsControl'

const listUserSettings = vi.hoisted(() => vi.fn().mockResolvedValue({ descriptors: [], values: [] }))
const updateUserSetting = vi.hoisted(() => vi.fn().mockResolvedValue({}))
const resetUserSetting = vi.hoisted(() => vi.fn().mockResolvedValue({}))

vi.mock('~/api/clients', () => ({
  userClient: { listUserSettings, updateUserSetting, resetUserSetting },
  authClient: {},
}))

function keydown(init: KeyboardEventInit): void {
  fireEvent(window, new KeyboardEvent('keydown', { bubbles: true, cancelable: true, ...init }))
}

describe('buildCommandRows', () => {
  it('merges registered commands with defaults and marks overrides Custom', () => {
    const rows = buildCommandRows(
      WORKSPACE_KEYBINDINGS,
      [{ key: 'F9', command: 'app.newAgent' }],
      [{ id: 'app.newAgent', title: 'New Agent', category: 'App' }],
    )
    const newAgent = rows.find(r => r.command === 'app.newAgent')
    expect(newAgent?.customized).toBe(true)
    expect(newAgent?.keys).toContain('F9')
    expect(newAgent?.keys).not.toContain('$mod+n')
    const prefs = rows.find(r => r.command === 'app.openPreferences')
    expect(prefs?.customized).toBe(false)
    expect(prefs?.keys).toContain('$mod+Comma')
  })

  it('includes default-bound commands that are not registered, titled by id', () => {
    const rows = buildCommandRows(WORKSPACE_KEYBINDINGS, [], [])
    const prev = rows.find(r => r.command === 'app.previousTab')
    expect(prev?.title).toBe('app.previousTab')
    expect(prev?.keys).toContain('$mod+BracketLeft')
  })

  /**
   * A row carries its default `when`, and an override the user records inherits
   * it. That matters most for the steer chord: rebound with no clause, `$mod+
   * Enter` would resolve everywhere -- and because `activateBindings` calls
   * preventDefault on a command that resolves, it would swallow the composer's
   * own send rather than fall through to it.
   */
  it('carries the steer binding\'s when-clause onto its row', () => {
    const rows = buildCommandRows(WORKSPACE_KEYBINDINGS, [], [
      { id: 'chat.steerQueuedInput', title: 'Steer Queued Input', category: 'Chat' },
    ])
    const steer = rows.find(r => r.command === 'chat.steerQueuedInput')
    expect(steer?.keys).toContain('$mod+Enter')
    expect(steer?.when).toBe('activeTabType == "agent" && chatInputEmpty && !terminalFocused && !dialogOpen')
  })

  // `chat.sendMessage` gave up $mod+j and kept no default chord, but it stays
  // registered and rebindable -- so Preferences must still list it, with its
  // real title and an empty binding for the user to fill in.
  it('lists a registered command that has no default binding at all', () => {
    const rows = buildCommandRows(WORKSPACE_KEYBINDINGS, [], [
      { id: 'chat.sendMessage', title: 'Send Message', category: 'Chat' },
    ])
    const send = rows.find(r => r.command === 'chat.sendMessage')
    expect(send?.title).toBe('Send Message')
    expect(send?.keys).toEqual([])
    expect(send?.customized).toBe(false)
  })
})

describe('chordFromEvent', () => {
  it('maps letters, modifiers, and named keys to the tinykeys convention', () => {
    expect(chordFromEvent(new KeyboardEvent('keydown', { key: 'n' }))).toBe('n')
    expect(chordFromEvent(new KeyboardEvent('keydown', { key: 'N', shiftKey: true }))).toBe('Shift+n')
    expect(chordFromEvent(new KeyboardEvent('keydown', { key: 'F9' }))).toBe('F9')
    // Bare modifier presses are still composing, not a chord.
    expect(chordFromEvent(new KeyboardEvent('keydown', { key: 'Meta' }))).toBeNull()
  })

  it('uses $mod for the platform modifier and event.code for punctuation', () => {
    const mac = getPlatform() === 'mac'
    const chord = chordFromEvent(new KeyboardEvent('keydown', { key: ',', code: 'Comma', [mac ? 'metaKey' : 'ctrlKey']: true }))
    expect(chord).toBe('$mod+Comma')
  })
})

describe('keybindingsControl', () => {
  beforeEach(() => {
    resetCommands()
    registerCommand({ id: 'app.newAgent', title: 'New Agent', category: 'App', handler: () => {} })
    registerCommand({ id: 'app.openPreferences', title: 'Open Preferences', category: 'App', handler: () => {} })
    listUserSettings.mockResolvedValue({ descriptors: [], values: [] })
    updateUserSetting.mockReset()
    updateUserSetting.mockResolvedValue({})
  })

  afterEach(() => {
    cleanup()
    resetCommands()
  })

  it('lists commands with their effective bindings and source badges', async () => {
    render(() => (
      <PreferencesProvider>
        <KeybindingsControl />
      </PreferencesProvider>
    ))
    await waitFor(() => expect(screen.getByText('New Agent')).toBeTruthy())
    expect(screen.getAllByText('Default').length).toBeGreaterThan(0)
    expect(screen.queryAllByText('Custom')).toHaveLength(0)
  })

  it('captures a clicked binding and writes the override', async () => {
    render(() => (
      <PreferencesProvider>
        <KeybindingsControl />
      </PreferencesProvider>
    ))
    await waitFor(() => expect(screen.getByText('New Agent')).toBeTruthy())
    fireEvent.click(screen.getByTestId('keybinding-app.newAgent'))
    await waitFor(() => expect(screen.getByTestId('keybinding-capture-app.newAgent')).toBeTruthy())

    keydown({ key: 'F9' })
    await waitFor(() => expect(updateUserSetting).toHaveBeenCalledWith({
      key: 'keybindings',
      partialJson: JSON.stringify([{ key: 'F9', command: 'app.newAgent', when: '!dialogOpen' }]),
    }))
    await waitFor(() => expect(screen.getAllByText('Custom').length).toBeGreaterThan(0))
  })

  it('refuses a chord already bound in the same when-context, naming the conflict', async () => {
    render(() => (
      <PreferencesProvider>
        <KeybindingsControl />
      </PreferencesProvider>
    ))
    await waitFor(() => expect(screen.getByText('New Agent')).toBeTruthy())
    fireEvent.click(screen.getByTestId('keybinding-app.openPreferences'))
    await waitFor(() => expect(screen.getByTestId('keybinding-capture-app.openPreferences')).toBeTruthy())

    // app.openPreferences has no when-clause; so does app.previousTab, the
    // default holder of $mod+BracketLeft — a genuine same-context conflict.
    const mac = getPlatform() === 'mac'
    keydown({ key: '[', code: 'BracketLeft', [mac ? 'metaKey' : 'ctrlKey']: true })
    await waitFor(() => expect(screen.getByTestId('keybinding-error').textContent).toContain('app.previousTab'))
    expect(updateUserSetting).not.toHaveBeenCalled()
  })

  it('resets a customized row', async () => {
    const overrides: UserKeybindingOverride[] = [{ key: 'F9', command: 'app.newAgent', when: '!dialogOpen' }]
    listUserSettings.mockResolvedValue({
      descriptors: [],
      values: [{ key: 'keybindings', valueJson: JSON.stringify(overrides), effectiveJson: JSON.stringify(overrides), customized: true, secretSet: {} }],
    })
    render(() => (
      <PreferencesProvider>
        <KeybindingsControl />
      </PreferencesProvider>
    ))
    await waitFor(() => expect(screen.getByTestId('keybinding-reset-app.newAgent')).toBeTruthy())
    fireEvent.click(screen.getByTestId('keybinding-reset-app.newAgent'))
    await waitFor(() => expect(updateUserSetting).toHaveBeenCalledWith({
      key: 'keybindings',
      partialJson: '[]',
    }))
  })
})
