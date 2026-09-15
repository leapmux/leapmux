import type { ToolPresentation } from '~/components/chat/results/toolPresentation'
import { describe, expect, it } from 'vitest'
import { acpToolPresentation } from '../acp/toolPresentation'
import { gooseToolAdapter } from './toolPresentation'

function delegate(rawInput: Record<string, unknown>, tool: Record<string, unknown> = {}): ToolPresentation {
  return acpToolPresentation({
    sessionUpdate: 'tool_call',
    toolCallId: 'goose-tool',
    status: 'pending',
    kind: 'other',
    title: 'delegate',
    _meta: { goose: { toolCall: { toolName: 'delegate', extensionName: 'summon' } } },
    rawInput,
    ...tool,
  }, gooseToolAdapter)
}

describe('gooseToolAdapter subagent launches', () => {
  it('titles the row from the instructions', () => {
    const presentation = delegate({ instructions: 'Inspect project structure', source: 'explore' })
    expect(presentation.kind).toBe('agent')
    expect(presentation.title).toBe('Inspect project structure')
    expect(presentation.agentRequest?.agentType).toBe('explore')
    expect(presentation.body).toEqual({ type: 'text' })
  })

  // Goose states its own fallback, so the shared `Task` never reaches this row.
  it('keeps the fallback Goose gives a launch that carries nothing', () => {
    expect(delegate({}).title).toBe('Delegate task')
  })

  it('draws the report once the call finished', () => {
    const presentation = delegate({ instructions: 'Inspect project structure' }, {
      sessionUpdate: 'tool_call_update',
      status: 'completed',
      content: [{ type: 'content', content: { type: 'text', text: 'Found two' } }],
    })
    expect(presentation.body).toEqual({ type: 'agent', source: expect.objectContaining({ description: 'Inspect project structure', body: 'Found two' }) })
  })
})
