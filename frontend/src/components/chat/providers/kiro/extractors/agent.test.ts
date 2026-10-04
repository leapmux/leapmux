import { describe, expect, it } from 'vitest'
import { acpToolFacts } from '../../acp/extractors/toolCall'
import { kiroAgentRequest, kiroAgentRun } from './agent'

/** The facts of one finished spawn, with its text and its raw output. */
function finished(text: string, rawOutput?: unknown, status = 'completed') {
  return acpToolFacts({
    sessionUpdate: 'tool_call_update',
    toolCallId: 'call',
    status,
    content: text ? [{ type: 'content', content: { type: 'text', text } }] : [],
    ...(rawOutput !== undefined ? { rawOutput } : {}),
  })
}

describe('kiroAgentRequest', () => {
  it('states the agent, the task and the reason of the spawn', () => {
    expect(kiroAgentRequest({ name: 'context-gatherer', prompt: 'Look.', explanation: ' Needs context ' }, 'Sub-agent: other', 'call-1')).toEqual({
      description: 'context-gatherer',
      agentType: 'context-gatherer',
      prompt: 'Look.',
      metadata: [{ label: 'Reason', value: 'Needs context' }],
      registryKey: 'call-1',
    })
  })

  it('reads the agent from the title when the arguments state none', () => {
    expect(kiroAgentRequest({ prompt: 'Look.' }, 'Sub-agent: helper', 'c')).toMatchObject({ description: 'helper', agentType: 'helper' })
  })

  it('titles a spawn that states no agent with a plain word, and omits what it lacks', () => {
    expect(kiroAgentRequest({ explanation: '   ' }, 'Something else', '')).toEqual({ description: 'Subagent', prompt: '' })
  })
})

describe('kiroAgentRun', () => {
  const request = { description: 'helper', prompt: 'go', registryKey: 'call-1' }

  it('states the string answer of the spawn as its report', () => {
    expect(kiroAgentRun(finished('Summary for the model', 'The **answer**'), request, 'sub-1')).toEqual({
      description: 'helper',
      registryKey: 'call-1',
      agentId: 'sub-1',
      metadata: [],
      outcome: 'completed',
      body: 'The **answer**',
    })
  })

  it('states the text when the raw output is no string', () => {
    expect(kiroAgentRun(finished('Found it.', { response: 'x' }), request, 'sub-1')).toMatchObject({ outcome: 'completed', body: 'Found it.' })
  })

  it('keeps the outcome of a failed or stopped spawn, with its text as the reason', () => {
    expect(kiroAgentRun(finished('Boom', 'Old answer', 'failed'), request, 'sub-1')).toMatchObject({ outcome: 'failed', body: 'Boom', agentId: 'sub-1' })
    expect(kiroAgentRun(finished('Cancelled', undefined, 'cancelled'), request, 'sub-1')).toMatchObject({ outcome: 'stopped', body: 'Cancelled' })
  })

  it('carries no registry key when the spawn had none', () => {
    const run = kiroAgentRun(finished('x', 'y'), { description: 'd', prompt: '' }, '')
    expect(Object.hasOwn(run, 'registryKey')).toBe(false)
    expect(run.agentId).toBe('')
  })
})
