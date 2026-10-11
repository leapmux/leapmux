import { describe, expect, it } from 'vitest'
import { codewhaleAgentRequest, codewhaleAgentRuns } from './agent'

describe('codewhaleAgentRequest', () => {
  it('describes a start by the child\'s name, then its type', () => {
    expect(codewhaleAgentRequest({ action: 'start', name: 'counter', type: 'explore', prompt: 'Count.' })).toStrictEqual({ description: 'counter', agentType: 'explore', prompt: 'Count.' })
    expect(codewhaleAgentRequest({ type: 'explore', prompt: 'Count.' })).toStrictEqual({ description: 'explore', agentType: 'explore', prompt: 'Count.' })
  })

  it('describes a management action by the ids it acts on', () => {
    expect(codewhaleAgentRequest({ action: 'wait', agent_ids: ['a1', 'a2', 3] })).toStrictEqual({ description: 'wait a1, a2', prompt: '' })
    expect(codewhaleAgentRequest({ action: 'cancel', agent_id: 'a1' })).toStrictEqual({ description: 'cancel a1', prompt: '' })
  })

  it('takes the single id over the list, and describes an action with no target by its word', () => {
    expect(codewhaleAgentRequest({ action: 'wait', agent_id: 'a1', agent_ids: ['a2'] }).description).toBe('wait a1')
    expect(codewhaleAgentRequest({ action: 'wait', agent_ids: 'a1' }).description).toBe('wait')
    expect(codewhaleAgentRequest({ action: 'wait', agent_ids: ['', ''] }).description).toBe('wait')
  })

  it('describes a start that names neither the child nor its type as nothing', () => {
    expect(codewhaleAgentRequest({})).toStrictEqual({ description: '', prompt: '' })
  })
})

describe('codewhaleAgentRuns', () => {
  const START = { description: 'counter', prompt: 'Count.' }

  it('reads the launched child and every status word', () => {
    // The child's own words, then the live words of a status record.
    const cases: Array<[string, string]> = [
      ['running', 'running'],
      ['completed', 'completed'],
      ['interrupted', 'stopped'],
      ['failed', 'failed'],
      ['cancelled', 'stopped'],
      ['budget_exhausted', 'failed'],
      ['queued', 'running'],
      ['starting', 'running'],
      ['waiting_for_user', 'running'],
      ['model_wait', 'running'],
      ['running_tool', 'running'],
      ['a_later_word', 'unknown'],
    ]
    for (const [status, outcome] of cases)
      expect(codewhaleAgentRuns(JSON.stringify({ agent_id: 'a1', name: 'counter', status }), START)?.[0]?.outcome, status).toBe(outcome)
    expect(codewhaleAgentRuns(JSON.stringify({ agent_id: 'a1', status: 'waiting_for_user' }), START)?.[0]?.statusLabel).toBe('waiting for user')
  })

  it('describes a child by its role when the record gives no name', () => {
    expect(codewhaleAgentRuns(JSON.stringify({ agent_id: 'a1', role: 'reviewer', status: 'completed' }), START)?.[0]?.description).toBe('reviewer')
    expect(codewhaleAgentRuns(JSON.stringify({ agent_id: 'a1', name: 'counter', role: 'reviewer' }), START)?.[0]?.description).toBe('counter')
  })

  it('keeps each settled child that states an id, and drops the rest', () => {
    const text = JSON.stringify({ settled: [{ agent_id: 'a1', name: 'one', status: 'completed' }, 'x', { name: 'no id' }, { agent_id: 'a2', status: 'failed' }], note: 'Two settled.' })
    expect(codewhaleAgentRuns(text, { description: 'wait', prompt: '' })).toStrictEqual([
      { description: 'one', agentId: 'a1', statusLabel: 'completed', outcome: 'completed', metadata: [{ label: 'Agent ID', value: 'a1' }], body: 'Two settled.' },
      { description: '', agentId: 'a2', statusLabel: 'failed', outcome: 'failed', metadata: [{ label: 'Agent ID', value: 'a2' }], body: 'Two settled.' },
    ])
  })

  it('answers null for a JSON value that is not a record', () => {
    for (const text of ['null', '"a1"', '42', ''])
      expect(codewhaleAgentRuns(text, START), text).toBeNull()
  })

  it('states no prompt label for a launch that sent none', () => {
    expect(codewhaleAgentRuns(JSON.stringify({ agent_id: 'a1' }), { description: '', prompt: '' })?.[0]).toStrictEqual({ description: '', agentId: 'a1', outcome: 'unknown', metadata: [{ label: 'Agent ID', value: 'a1' }], body: '' })
  })

  it('answers null for text that names no child', () => {
    expect(codewhaleAgentRuns('Started', START)).toBeNull()
    expect(codewhaleAgentRuns('[]', START)).toBeNull()
    expect(codewhaleAgentRuns(JSON.stringify({ settled: [{ name: 'no id' }] }), START)).toBeNull()
    expect(codewhaleAgentRuns(JSON.stringify({ status: 'running' }), START)).toBeNull()
  })
})
