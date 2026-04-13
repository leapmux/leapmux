import type { Keybinding, UserKeybindingOverride } from './types'

import { afterEach, describe, expect, it } from 'vitest'
import { resetContext, setContext } from './context'
import { groupBindings, mergeKeybindings, resolve } from './keybindings'

afterEach(() => {
  resetContext()
})

const DEFAULTS: Keybinding[] = [
  { key: '$mod+n', command: 'app.newAgent', when: '!dialogOpen' },
  { key: '$mod+t', command: 'app.newTerminal', when: '!dialogOpen' },
  { key: '$mod+w', command: 'app.closeActiveTab' },
  { key: 'Escape', command: 'dialog.close', when: 'dialogOpen' },
  { key: '$mod+b', command: 'app.toggleLeftSidebar' },
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
      { key: '$mod+b', command: 'app.toggleLeftSidebar' },
    ]
    expect(resolve(bindings, '$mod+b')).toBe('app.toggleLeftSidebar')
  })
})
