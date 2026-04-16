import type { Keybinding, Platform } from './types'

const MODIFIER_MAP: Record<string, string> = {
  $mod: 'CmdOrCtrl',
  Meta: 'Super',
  Control: 'Ctrl',
  Ctrl: 'Ctrl',
  Shift: 'Shift',
  Alt: 'Alt',
  Option: 'Alt',
}

const SPECIAL_KEY_MAP: Record<string, string> = {
  'Escape': 'Escape',
  'Enter': 'Enter',
  'Backspace': 'Backspace',
  'Delete': 'Delete',
  'ArrowUp': 'ArrowUp',
  'ArrowDown': 'ArrowDown',
  'ArrowLeft': 'ArrowLeft',
  'ArrowRight': 'ArrowRight',
  'Tab': 'Tab',
  'PageUp': 'PageUp',
  'PageDown': 'PageDown',
  ' ': 'Space',
  'Space': 'Space',
  'Comma': 'Comma',
  'Period': 'Period',
  'Slash': 'Slash',
  'Backslash': 'Backslash',
  'BracketLeft': 'BracketLeft',
  'BracketRight': 'BracketRight',
}

const FUNCTION_KEY_RE = /^F(?:[1-9]|1[0-2])$/
const LETTER_KEY_RE = /^[a-z]$/i
const DIGIT_KEY_RE = /^\d$/

function keyCodeForTauri(key: string): string | undefined {
  if (FUNCTION_KEY_RE.test(key))
    return key
  if (LETTER_KEY_RE.test(key))
    return `Key${key.toUpperCase()}`
  if (DIGIT_KEY_RE.test(key))
    return `Digit${key}`
  return SPECIAL_KEY_MAP[key]
}

export function tinykeysToTauriAccelerator(key: string, _platform: Platform): string | undefined {
  if (key.includes(' '))
    return undefined

  const parts = key.split('+')
  const modifiers: string[] = []
  let mainKey: string | undefined

  for (const part of parts) {
    const mappedModifier = MODIFIER_MAP[part]
    if (mappedModifier) {
      if (!modifiers.includes(mappedModifier))
        modifiers.push(mappedModifier)
      continue
    }

    if (mainKey)
      return undefined

    mainKey = keyCodeForTauri(part)
    if (!mainKey)
      return undefined
  }

  if (!mainKey)
    return undefined

  const orderedModifiers = ['CmdOrCtrl', 'Super', 'Ctrl', 'Alt', 'Shift']
    .filter(modifier => modifiers.includes(modifier))

  return [...orderedModifiers, mainKey].join('+')
}

export function getPrimaryBindingForCommand(bindings: readonly Keybinding[], commandId: string): string | undefined {
  return bindings.find(binding => binding.command === commandId)?.key
}
