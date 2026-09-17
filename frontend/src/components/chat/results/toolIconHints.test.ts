import type { ToolIconHint } from '../ir/toolCall'
import { describe, expect, it } from 'vitest'
import { toolHintIcon } from './toolIconHints'

// Every hint the IR can state. Listed here rather than derived, because the
// union is a type and leaves nothing to read at run time: a hint added to it
// and forgotten here is what the count assertion below catches.
const EVERY_HINT: readonly ToolIconHint[] = [
  'checklist',
  'stop',
  'plan-enter',
  'plan-exit',
  'webhook',
  'branch',
  'json',
]

describe('toolHintIcon', () => {
  it('answers every hint with an icon', () => {
    for (const hint of EVERY_HINT)
      expect(toolHintIcon(hint), hint).toBeDefined()
  })

  // A call that states no hint takes the KIND's icon, which `ToolMessage`
  // supplies. Returning a glyph here would override the kind for every call.
  it('answers an absent hint with no icon', () => {
    expect(toolHintIcon(undefined)).toBeUndefined()
  })

  // The hints exist to say things a shared glyph cannot, so a map that answered
  // two of them the same way would give the reader back the ambiguity the hint
  // was added to remove.
  it('answers each hint with a distinct icon', () => {
    const icons = EVERY_HINT.map(hint => toolHintIcon(hint))
    expect(new Set(icons).size).toBe(EVERY_HINT.length)
  })

  // A hint the union gained and this file never listed would pass every case
  // above by never being tested.
  it('states every hint the union declares', () => {
    const declared: Record<ToolIconHint, true> = {
      'checklist': true,
      'stop': true,
      'plan-enter': true,
      'plan-exit': true,
      'webhook': true,
      'branch': true,
      'json': true,
    }
    expect([...EVERY_HINT].sort()).toEqual(Object.keys(declared).sort())
  })
})
