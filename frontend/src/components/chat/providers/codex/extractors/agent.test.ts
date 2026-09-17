import { describe, expect, it } from 'vitest'
import { input } from '../../testUtils'
import { codexAgentCounterpart, codexAgentRequest, codexAgentResults, resolveCodexAgentItem } from './agent'

const item = { id: 'call', type: 'collabAgentToolCall', tool: 'spawnAgent', status: 'completed' }

// The registry knows what the subagent DOES. `codexAgentRequest.description` is the
// tool's own label, so every spawn row read "Subagent" without this key.
describe('codexAgentRequest registry key', () => {
  it('states the one background task a spawn created', () => {
    expect(codexAgentRequest({ ...item, receiverThreadIds: ['child-1'] }).registryKey).toBe('child-1')
  })

  it.each([
    ['several targets', { tool: 'spawnAgent', receiverThreadIds: ['child-1', 'child-2'] }],
    ['no target', { tool: 'spawnAgent', receiverThreadIds: [] }],
    ['a tool that creates nothing', { tool: 'sendInput', receiverThreadIds: ['child-1'] }],
  ])('states none for %s', (_name, fields) => {
    expect(codexAgentRequest({ ...item, ...fields }).registryKey).toBeUndefined()
  })
})

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

  // Codex serializes an unset String as `""`, so an empty field is an ABSENT one and the
  // counterpart supplies it. A nullish test alone kept the empty string, and the result
  // row lost the prompt its own request carried.
  it('keeps the current status and report when request fields are recovered', () => {
    const request = { ...item, status: 'inProgress', prompt: 'Requested prompt', model: 'requested-model', agentsStates: { child: { status: 'running' } } }
    const current = { ...item, prompt: '', model: null, agentsStates: { child: { status: 'completed', message: 'Actual report' } } }
    const resolved = resolveCodexAgentItem(current, codexAgentCounterpart(current, input({ item: request }), 'request'))
    expect(resolved.prompt).toBe('Requested prompt')
    expect(resolved.model).toBe('requested-model')
    expect(resolved.status).toBe('completed')
    expect(codexAgentResults(resolved)[0]).toMatchObject({ outcome: 'completed', body: 'Actual report' })
    expect(current.model).toBeNull()
  })

  it.each(['tool', 'prompt', 'model', 'reasoningEffort'])('recovers %s from the counterpart when the current item carries an empty string', (field) => {
    const request = { ...item, status: 'inProgress', tool: 'spawnAgent', prompt: 'p', model: 'm', reasoningEffort: 'high' }
    const current = { ...item, [field]: '' }
    const resolved = resolveCodexAgentItem(current, codexAgentCounterpart(current, input({ item: request }), 'request'))
    expect(resolved[field]).toBe(request[field as keyof typeof request])
  })

  it('keeps a non-empty field of the current item, which is the authoritative one', () => {
    const request = { ...item, status: 'inProgress', prompt: 'Requested prompt' }
    const current = { ...item, prompt: 'Current prompt' }
    const resolved = resolveCodexAgentItem(current, codexAgentCounterpart(current, input({ item: request }), 'request'))
    expect(resolved.prompt).toBe('Current prompt')
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
