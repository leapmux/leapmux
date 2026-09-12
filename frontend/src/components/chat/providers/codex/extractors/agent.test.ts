import { describe, expect, it } from 'vitest'
import { input } from '../../testUtils'
import { codexAgentCounterpart, codexAgentResults, resolveCodexAgentItem } from './agent'

const item = { id: 'call', type: 'collabAgentToolCall', tool: 'spawnAgent', status: 'completed' }

describe('codex agent counterpart validation', () => {
  it.each([
    { id: 'other' },
    { id: '' },
    { id: 0 },
    { type: 'commandExecution' },
    { tool: 'wait' },
    { status: 'completed' },
  ])('rejects a different identity, kind, tool, or role: %j', (fields) => {
    expect(codexAgentCounterpart(item, input({ item: { ...item, status: 'inProgress', ...fields } }), 'request')).toBeNull()
  })

  it('keeps the current status and report when request fields are recovered', () => {
    const request = { ...item, status: 'inProgress', prompt: 'Requested prompt', model: 'requested-model', agentsStates: { child: { status: 'running' } } }
    const current = { ...item, prompt: '', model: null, agentsStates: { child: { status: 'completed', message: 'Actual report' } } }
    const resolved = resolveCodexAgentItem(current, codexAgentCounterpart(current, input({ item: request }), 'request'))
    expect(resolved.prompt).toBe('')
    expect(resolved.model).toBe('requested-model')
    expect(resolved.status).toBe('completed')
    expect(codexAgentResults(resolved)[0]).toMatchObject({ outcome: 'completed', body: 'Actual report' })
    expect(current.model).toBeNull()
  })

  it.each([
    ['pendingInit', 'running'],
    ['running', 'running'],
    ['completed', 'completed'],
    ['errored', 'failed'],
    ['interrupted', 'stopped'],
    ['shutdown', 'stopped'],
    ['notFound', 'failed'],
    ['newState', 'unknown'],
  ])('preserves the native child state %s', (status, outcome) => {
    const result = codexAgentResults({ ...item, agentsStates: { child: { status, message: 'Native message' } } })
    expect(result[0]).toMatchObject({ outcome, body: 'Native message' })
  })

  it('drops empty receiver IDs and does not duplicate receiver and state-map entries', () => {
    expect(codexAgentResults({ ...item, receiverThreadIds: ['', 'child', null, 'child'], agentsStates: { child: { status: 'completed' }, another: { status: 'running' } } }).map(result => result.agentId)).toEqual(['child', 'another'])
  })
})
