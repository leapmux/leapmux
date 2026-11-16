import { describe, expect, it } from 'vitest'
import { openCodeToolKind } from './toolKinds'
import { OPENCODE_TOOL_NAMES } from './toolNames'

describe('openCodeToolKind', () => {
  it('categorizes every installed native tool', () => {
    for (const name of Object.values(OPENCODE_TOOL_NAMES))
      expect(openCodeToolKind(name)).not.toBe('mcp')
  })

  it('keeps custom and prototype-shaped names on the generic reader', () => {
    for (const name of [
      '',
      'custom',
      'constructor',
      'toString',
      '__proto__',
    ])
      expect(openCodeToolKind(name)).toBe('mcp')
  })
})
