import type { MockModelRequestRecord, MockModelToolCall } from '../helpers/mockModelScript'
import { describe, expect, it } from 'vitest'
import { childSpawnCall, childSpawnResult } from './childScenarios'

// The first lines of an actual native Task result. An E2E run of subagent-transcript-tab.spec.ts recorded them in the
// root request after the Task call. Droid kept the ID of the built call and gave its result under that ID.
const LAUNCHED = [
  'Task launched in background.',
  'task_id: 4bfae727-ceeb-44d0-9afb-2fc4609d439a',
  'session_id: 4bfae727-ceeb-44d0-9afb-2fc4609d439a',
  'subagent_type: explorer',
  'description: Inspect the child note',
  'The task is running in a subagent session.',
].join('\n')

const spawn = childSpawnCall('Read the child note and report its marker.')

function assistantCall(call: MockModelToolCall): Record<string, unknown> {
  return { role: 'assistant', content: null, tool_calls: [{ id: call.id, type: 'function', function: { name: call.name, arguments: JSON.stringify(call.arguments) } }] }
}

function taskResult(id: string): Record<string, unknown> {
  return { role: 'tool', tool_call_id: id, content: LAUNCHED }
}

function rootRequest(...messages: Record<string, unknown>[]): MockModelRequestRecord {
  return {
    protocol: 'openai-chat-completions',
    path: '/v1/chat/completions',
    stepIndex: 1,
    body: { messages: [{ role: 'user', content: 'Delegate the note inspection to a background child.' }, ...messages] },
  }
}

describe('childSpawnCall', () => {
  it('sends a background Task under the call_ ID that Droid keeps', () => {
    expect(spawn).toMatchObject({ id: 'call_droid-spawn', name: 'Task', arguments: { description: 'Inspect the child note', await: false } })
  })
})

describe('childSpawnResult', () => {
  it('reads the result that Droid gave under the ID of the built Task', () => {
    expect(childSpawnResult(rootRequest(assistantCall(spawn), taskResult(spawn.id)), spawn)).toBe(LAUNCHED)
  })

  it('refuses a request whose Task holds no result', () => {
    expect(() => childSpawnResult(rootRequest(assistantCall(spawn)), spawn)).toThrow('0 results')
  })

  it('refuses a request that holds two results for the Task', () => {
    expect(() => childSpawnResult(rootRequest(assistantCall(spawn), taskResult(spawn.id), taskResult(spawn.id)), spawn)).toThrow('2 results')
  })

  it('refuses a result of another tool under the ID of the Task', () => {
    const read = { ...spawn, name: 'Read' }
    expect(() => childSpawnResult(rootRequest(assistantCall(read), taskResult(read.id)), spawn)).toThrow('does not match the requested native tool')
  })

  it('refuses a Task under the scripted ID, which the builder never sends', () => {
    const unprefixed = { ...spawn, id: 'droid-spawn' }
    expect(() => childSpawnResult(rootRequest(assistantCall(unprefixed), taskResult(unprefixed.id)), spawn)).toThrow('0 tool calls')
  })

  it('refuses a request with no messages', () => {
    expect(() => childSpawnResult({ ...rootRequest(), body: {} }, spawn)).toThrow('actual Chat model messages')
  })
})
