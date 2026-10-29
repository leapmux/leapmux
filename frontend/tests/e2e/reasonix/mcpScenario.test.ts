import { describe, expect, it } from 'vitest'
import { elicitationFieldKey } from '../../../src/components/chat/controls/elicitationForm'
import { reasonixMcpFormDraftSaved } from './mcpScenario'

function draft() {
  return {
    choices: { [elicitationFieldKey('count')]: '0', [elicitationFieldKey('enabled')]: '"No"', [elicitationFieldKey('color')]: '"Blue"' },
  }
}

describe('reasonixMcpFormDraftSaved', () => {
  it('requires the exact native text and display-label answers', () => {
    expect(reasonixMcpFormDraftSaved(draft())).toBe(true)
  })

  it.each([
    null,
    undefined,
    {},
    [],
    { choices: null },
    { choices: {} },
  ])('rejects a missing or malformed draft: %j', (value) => {
    expect(reasonixMcpFormDraftSaved(value)).toBe(false)
  })

  it.each(['count', 'enabled', 'color'])('rejects an absent or changed %s answer', (field) => {
    const value = draft()
    delete value.choices[elicitationFieldKey(field)]
    expect(reasonixMcpFormDraftSaved(value)).toBe(false)
    value.choices[elicitationFieldKey(field)] = '"another answer"'
    expect(reasonixMcpFormDraftSaved(value)).toBe(false)
  })

  it('rejects a JSON-encoded text input instead of its actual raw draft', () => {
    const value = draft()
    value.choices[elicitationFieldKey('count')] = '"0"'
    expect(reasonixMcpFormDraftSaved(value)).toBe(false)
  })

  it('rejects the original MCP types that the native transport does not send', () => {
    expect(reasonixMcpFormDraftSaved({
      choices: { [elicitationFieldKey('count')]: '0', [elicitationFieldKey('enabled')]: 'false', [elicitationFieldKey('color')]: '"b"' },
    })).toBe(false)
  })
})
