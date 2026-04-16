import { describe, expect, it } from 'vitest'
import { getPrimaryBindingForCommand, tinykeysToTauriAccelerator } from './tauriAccelerator'

describe('tinykeysToTauriAccelerator', () => {
  it('converts a command-option letter shortcut for macOS', () => {
    expect(tinykeysToTauriAccelerator('$mod+Alt+i', 'mac')).toBe('CmdOrCtrl+Alt+KeyI')
  })

  it('passes through function keys', () => {
    expect(tinykeysToTauriAccelerator('F12', 'mac')).toBe('F12')
  })

  it('returns undefined for multi-chord shortcuts', () => {
    expect(tinykeysToTauriAccelerator('$mod+k $mod+s', 'mac')).toBeUndefined()
  })
})

describe('getPrimaryBindingForCommand', () => {
  it('returns the first configured binding for a command', () => {
    expect(getPrimaryBindingForCommand([
      { key: '$mod+Alt+i', command: 'app.openWebInspector' },
      { key: 'F12', command: 'app.openWebInspector' },
    ], 'app.openWebInspector')).toBe('$mod+Alt+i')
  })
})
