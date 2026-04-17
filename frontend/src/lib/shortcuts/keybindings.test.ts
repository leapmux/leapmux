import type { Keybinding, UserKeybindingOverride } from './types'

import { afterEach, describe, expect, it, vi } from 'vitest'
import { registerCommand, resetCommands } from './commands'
import { resetContext, setContext } from './context'
import { activateBindings, groupBindings, mergeKeybindings, resolve, unbindAll } from './keybindings'

afterEach(() => {
  resetContext()
})

const DEFAULTS: Keybinding[] = [
  { key: '$mod+n', command: 'app.newAgent', when: '!dialogOpen' },
  { key: '$mod+t', command: 'app.newTerminal', when: '!dialogOpen' },
  { key: '$mod+w', command: 'app.closeActiveTab' },
  { key: 'Escape', command: 'dialog.close', when: 'dialogOpen' },
  { key: '$mod+Shift+BracketLeft', command: 'app.toggleLeftSidebar' },
]

describe('mergeKeybindings', () => {
  it('returns defaults unchanged when no overrides', () => {
    const result = mergeKeybindings(DEFAULTS, [])
    expect(result).toEqual(DEFAULTS)
  })

  it('overrides key for existing command', () => {
    const overrides: UserKeybindingOverride[] = [
      { key: '$mod+Shift+a', command: 'app.newAgent' },
    ]
    const result = mergeKeybindings(DEFAULTS, overrides)
    const newAgent = result.find(b => b.command === 'app.newAgent')
    expect(newAgent!.key).toBe('$mod+Shift+a')
    // Preserves original when-clause
    expect(newAgent!.when).toBe('!dialogOpen')
  })

  it('overrides when clause if provided', () => {
    const overrides: UserKeybindingOverride[] = [
      { key: '$mod+n', command: 'app.newAgent', when: 'editorFocused' },
    ]
    const result = mergeKeybindings(DEFAULTS, overrides)
    const newAgent = result.find(b => b.command === 'app.newAgent')
    expect(newAgent!.when).toBe('editorFocused')
  })

  it('removes binding when override key is empty string', () => {
    const overrides: UserKeybindingOverride[] = [
      { key: '', command: 'app.newAgent' },
    ]
    const result = mergeKeybindings(DEFAULTS, overrides)
    expect(result.find(b => b.command === 'app.newAgent')).toBeUndefined()
    expect(result).toHaveLength(DEFAULTS.length - 1)
  })

  it('appends new command bindings from overrides', () => {
    const overrides: UserKeybindingOverride[] = [
      { key: '$mod+Shift+p', command: 'app.commandPalette' },
    ]
    const result = mergeKeybindings(DEFAULTS, overrides)
    expect(result).toHaveLength(DEFAULTS.length + 1)
    const palette = result.find(b => b.command === 'app.commandPalette')
    expect(palette!.key).toBe('$mod+Shift+p')
  })

  it('ignores new command with empty key', () => {
    const overrides: UserKeybindingOverride[] = [
      { key: '', command: 'app.commandPalette' },
    ]
    const result = mergeKeybindings(DEFAULTS, overrides)
    expect(result).toHaveLength(DEFAULTS.length)
  })

  it('supports multiple overrides for the same command', () => {
    const overrides: UserKeybindingOverride[] = [
      { key: '$mod+n', command: 'app.newAgent', when: '!dialogOpen' },
      { key: '$mod+Shift+n', command: 'app.newAgent', when: 'editorFocused' },
    ]
    const result = mergeKeybindings(DEFAULTS, overrides)
    const newAgentBindings = result.filter(b => b.command === 'app.newAgent')
    expect(newAgentBindings).toHaveLength(2)
    expect(newAgentBindings[0].key).toBe('$mod+n')
    expect(newAgentBindings[0].when).toBe('!dialogOpen')
    expect(newAgentBindings[1].key).toBe('$mod+Shift+n')
    expect(newAgentBindings[1].when).toBe('editorFocused')
  })

  it('inherits default when-clause for multi-override entries without when', () => {
    const overrides: UserKeybindingOverride[] = [
      { key: '$mod+Shift+a', command: 'app.newAgent' },
      { key: '$mod+Alt+a', command: 'app.newAgent', when: 'editorFocused' },
    ]
    const result = mergeKeybindings(DEFAULTS, overrides)
    const newAgentBindings = result.filter(b => b.command === 'app.newAgent')
    expect(newAgentBindings).toHaveLength(2)
    expect(newAgentBindings[0].when).toBe('!dialogOpen') // inherited from default
    expect(newAgentBindings[1].when).toBe('editorFocused') // explicit
  })

  it('unbinds default when all overrides have empty key', () => {
    const overrides: UserKeybindingOverride[] = [
      { key: '', command: 'app.newAgent' },
      { key: '', command: 'app.newAgent' },
    ]
    const result = mergeKeybindings(DEFAULTS, overrides)
    expect(result.filter(b => b.command === 'app.newAgent')).toHaveLength(0)
  })

  it('filters empty keys from mixed multi-overrides', () => {
    const overrides: UserKeybindingOverride[] = [
      { key: '', command: 'app.newAgent' },
      { key: '$mod+Shift+n', command: 'app.newAgent', when: 'editorFocused' },
    ]
    const result = mergeKeybindings(DEFAULTS, overrides)
    const newAgentBindings = result.filter(b => b.command === 'app.newAgent')
    expect(newAgentBindings).toHaveLength(1)
    expect(newAgentBindings[0].key).toBe('$mod+Shift+n')
  })

  it('supports multiple overrides for new commands not in defaults', () => {
    const overrides: UserKeybindingOverride[] = [
      { key: '$mod+Shift+p', command: 'app.commandPalette', when: '!dialogOpen' },
      { key: '$mod+p', command: 'app.commandPalette', when: 'editorFocused' },
    ]
    const result = mergeKeybindings(DEFAULTS, overrides)
    const paletteBindings = result.filter(b => b.command === 'app.commandPalette')
    expect(paletteBindings).toHaveLength(2)
  })

  it('does not mutate the defaults array', () => {
    const original = DEFAULTS.map(b => ({ ...b }))
    mergeKeybindings(DEFAULTS, [{ key: '$mod+Shift+a', command: 'app.newAgent' }])
    expect(DEFAULTS).toEqual(original)
  })
})

describe('groupBindings', () => {
  it('groups bindings by key string', () => {
    const bindings: Keybinding[] = [
      { key: '$mod+n', command: 'a' },
      { key: '$mod+n', command: 'b', when: 'editorFocused' },
      { key: '$mod+t', command: 'c' },
    ]
    const groups = groupBindings(bindings)
    expect(groups).toHaveLength(2)
    const nGroup = groups.find(g => g.key === '$mod+n')!
    expect(nGroup.bindings).toHaveLength(2)
    const tGroup = groups.find(g => g.key === '$mod+t')!
    expect(tGroup.bindings).toHaveLength(1)
  })
})

describe('resolve', () => {
  it('returns first matching command', () => {
    const bindings: Keybinding[] = [
      { key: 'Escape', command: 'dialog.close', when: 'dialogOpen' },
      { key: 'Escape', command: 'app.blur' },
    ]
    setContext('dialogOpen', true)
    expect(resolve(bindings, 'Escape')).toBe('dialog.close')
  })

  it('skips bindings with failing when clause', () => {
    const bindings: Keybinding[] = [
      { key: 'Escape', command: 'dialog.close', when: 'dialogOpen' },
      { key: 'Escape', command: 'app.blur' },
    ]
    setContext('dialogOpen', false)
    expect(resolve(bindings, 'Escape')).toBe('app.blur')
  })

  it('returns null when no binding matches', () => {
    const bindings: Keybinding[] = [
      { key: 'Escape', command: 'dialog.close', when: 'dialogOpen' },
    ]
    setContext('dialogOpen', false)
    expect(resolve(bindings, 'Escape')).toBeNull()
  })

  it('returns command when no when clause is set', () => {
    const bindings: Keybinding[] = [
      { key: '$mod+Shift+BracketLeft', command: 'app.toggleLeftSidebar' },
    ]
    expect(resolve(bindings, '$mod+Shift+BracketLeft')).toBe('app.toggleLeftSidebar')
  })

  it('suppresses plain non-modifier keys when input is focused', () => {
    const bindings: Keybinding[] = [
      { key: 'x', command: 'app.example' },
    ]
    setContext('inputFocused', true)
    expect(resolve(bindings, 'x')).toBeNull()
  })

  it('allows plain function keys when input is focused', () => {
    const bindings: Keybinding[] = [
      { key: 'F5', command: 'app.refreshDirectoryTree' },
    ]
    setContext('inputFocused', true)
    expect(resolve(bindings, 'F5')).toBe('app.refreshDirectoryTree')
  })

  it('still suppresses other plain special keys when input is focused', () => {
    const bindings: Keybinding[] = [
      { key: 'Escape', command: 'app.example' },
    ]
    setContext('inputFocused', true)
    expect(resolve(bindings, 'Escape')).toBeNull()
  })
})

describe('activateBindings (Mac Option dead-key)', () => {
  afterEach(() => {
    unbindAll()
    resetCommands()
  })

  // On macOS WebKit, Option+N produces event.key='\u02DC' (dead-key for tilde)
  // even when Command is also pressed. tinykeys matches literal letters against
  // event.key, so a raw 'n' binding misses. Matching via event.code='KeyN'
  // keeps the shortcut working regardless of Option's text transformation or
  // the active input method.
  it('fires an Alt+letter binding when Option transforms event.key', () => {
    const handler = vi.fn()
    resetCommands()
    registerCommand({ id: 'test.deadKey', title: 'Test', handler })

    activateBindings([{ key: 'Alt+n', command: 'test.deadKey' }])

    window.dispatchEvent(new KeyboardEvent('keydown', {
      key: '\u02DC',
      code: 'KeyN',
      altKey: true,
    }))

    expect(handler).toHaveBeenCalledTimes(1)
  })
})
