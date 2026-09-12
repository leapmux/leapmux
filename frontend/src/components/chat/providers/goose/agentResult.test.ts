import { describe, expect, it } from 'vitest'
import { gooseAgentRequest, gooseAgentResult } from './agentResult'

describe('goose agent result', () => {
  it('keeps reference context and zero maximum turns in the request', () => {
    const source = gooseAgentRequest({ instructions: 'Inspect code', context: 'Keep this context', max_turns: 0 })
    expect(source.prompt).toContain('Keep this context')
    expect(source.prompt).toContain('Inspect code')
    expect(source.metadata).toContainEqual({ label: 'Maximum turns', value: '0' })
  })

  it('does not remove an acknowledgement with different session IDs', () => {
    const output = 'Task first started in background: "Work"\nContinue with other work. When you need the result, use load(source: "second").'
    expect(gooseAgentResult({ async: true }, output, 'completed')).toMatchObject({ outcome: 'unknown', body: output })
  })

  it('keeps a failed delegation message', () => {
    expect(gooseAgentResult({ async: true, instructions: 'Prompt' }, 'Delegation failed: unavailable', 'failed')).toMatchObject({ outcome: 'failed', body: 'Delegation failed: unavailable' })
  })
})
