import { describe, expect, it } from 'vitest'
import { gooseSubagentToolRequestName, isGooseSubagentToolRequest } from './subagentToolRequest'

describe('isGooseSubagentToolRequest', () => {
  // Each short-circuit branch of the shape detector gets its own case, so a
  // regression on one nesting level shows exactly where it is.
  it('returns false when _meta is absent', () => {
    expect(isGooseSubagentToolRequest({ sessionUpdate: 'tool_call_update' })).toBe(false)
  })

  it('returns false when toolNotification.type is not "message"', () => {
    expect(isGooseSubagentToolRequest({ _meta: { toolNotification: { type: 'log', params: { data: { type: 'subagent_tool_request' } } } } })).toBe(false)
  })

  it('returns false when the data discriminator is not subagent_tool_request', () => {
    expect(isGooseSubagentToolRequest({ _meta: { toolNotification: { type: 'message', params: { data: { type: 'other' } } } } })).toBe(false)
  })

  it('returns true for the full shape', () => {
    expect(isGooseSubagentToolRequest({ _meta: { toolNotification: { type: 'message', params: { data: { type: 'subagent_tool_request', tool_call: { name: 'Read' } } } } } })).toBe(true)
  })
})

describe('gooseSubagentToolRequestName', () => {
  it('extracts the tool_call name', () => {
    const parent = {
      _meta: {
        toolNotification: {
          type: 'message',
          params: { data: { type: 'subagent_tool_request', tool_call: { name: 'Bash' } } },
        },
      },
    }
    expect(gooseSubagentToolRequestName(parent)).toBe('Bash')
  })

  it('returns empty string when the shape is absent', () => {
    expect(gooseSubagentToolRequestName({})).toBe('')
  })
})
