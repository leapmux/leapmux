import { describe, expect, it } from 'vitest'
import { claudeWorkflowLaunch, claudeWorkflowModelOutcome, claudeWorkflowOutput, claudeWorkflowOutputFile, claudeWorkflowSnapshot } from './codeExecution'

const launch = { callId: 'call', taskId: 'task', runId: 'run' }

function modelNotification(status = 'completed', result = '&quot;MARKER42&quot;'): unknown {
  return { messages: [{ role: 'user', content: [{ type: 'text', text: [
    '<task-notification>',
    '<task-id>task</task-id>',
    '<tool-use-id>call</tool-use-id>',
    '<output-file>/native/task.output</output-file>',
    `<status>${status}</status>`,
    '<summary>Native script error MARKER77</summary>',
    ...(status === 'completed' ? [`<result>${result}</result>`] : []),
    '</task-notification>',
  ].join('\n') }] }] }
}

describe('claudeWorkflowModelOutcome', () => {
  it.each(['', 0, false, null])('preserves a serialized native result: %j', (result) => {
    expect(claudeWorkflowModelOutcome([modelNotification('completed', JSON.stringify(result))], launch)?.result).toEqual(result)
  })

  it('reads computed output only from the exact native user task notification', () => {
    expect(claudeWorkflowModelOutcome([], launch)).toBeUndefined()
    expect(claudeWorkflowModelOutcome([modelNotification()], launch)).toEqual({ status: 'completed', summary: 'Native script error MARKER77', outputFile: '/native/task.output', result: 'MARKER42' })
    expect(claudeWorkflowModelOutcome([modelNotification()], { ...launch, taskId: 'other' })).toBeUndefined()
    expect(claudeWorkflowModelOutcome([modelNotification()], { ...launch, callId: 'other' })).toBeUndefined()
    const text: unknown = JSON.parse(JSON.stringify(modelNotification()).replace('"role":"user"', '"role":"assistant"'))
    expect(claudeWorkflowModelOutcome([text], launch)).toBeUndefined()
  })

  it('preserves a failed notification without requiring a success result or file read', () => {
    expect(claudeWorkflowModelOutcome([modelNotification('failed')], launch)).toEqual({ status: 'failed', summary: 'Native script error MARKER77', outputFile: '/native/task.output' })
    expect(claudeWorkflowModelOutcome([modelNotification(), modelNotification()], launch)?.result).toBe('MARKER42')
    expect(() => claudeWorkflowModelOutcome([modelNotification(), modelNotification('failed')], launch)).toThrow('different final outcome')
  })

  it('rejects running status, malformed JSON, and truncated native result text', () => {
    expect(() => claudeWorkflowModelOutcome([modelNotification('running')], launch)).toThrow('final native outcome')
    expect(() => claudeWorkflowModelOutcome([modelNotification('completed', '{')], launch)).toThrow(SyntaxError)
    expect(() => claudeWorkflowModelOutcome([modelNotification('completed', '... (truncated)')], launch)).toThrow(SyntaxError)
  })
})

describe('claudeWorkflowLaunch', () => {
  it('accepts an exact structured launch wrapper but rejects duplicate call results', () => {
    const result = { status: 'async_launched', taskId: 'task', runId: 'run' }
    const block = { type: 'tool_result', tool_use_id: 'call' }
    expect(claudeWorkflowLaunch({ message: { content: [block] }, tool_use_result: { data: result } }, 'call')).toEqual(launch)
    expect(claudeWorkflowLaunch({ message: { content: [block, block] }, tool_use_result: result }, 'call')).toBeUndefined()
  })

  it('keeps structured native identity and refuses another original call', () => {
    const value = { message: { content: [{ type: 'tool_result', tool_use_id: 'call' }] }, tool_use_result: { status: 'async_launched', taskId: 'task', runId: 'run' } }
    expect(claudeWorkflowLaunch(value, 'call')).toEqual(launch)
    expect(claudeWorkflowLaunch(value, 'other')).toBeUndefined()
  })

  it('rejects a compile failure and empty run identity', () => {
    const value = { message: { content: [{ type: 'tool_result', tool_use_id: 'call' }] }, tool_use_result: { status: 'async_launched', taskId: 'task', runId: 'run', error: 'Compile failed' } }
    expect(claudeWorkflowLaunch(value, 'call')).toBeUndefined()
    expect(claudeWorkflowLaunch({ ...value, tool_use_result: { status: 'async_launched', taskId: '', runId: '' } }, 'call')).toBeUndefined()
  })
})

describe('claudeWorkflowSnapshot', () => {
  it.each(['', 0, false, null])('preserves an exact completed snapshot value: %j', (result) => {
    expect(claudeWorkflowSnapshot({ ...launch, status: 'completed', result }, launch)).toEqual({ status: 'completed', result })
  })

  it('requires the exact run and task for a native script failure', () => {
    const value = { ...launch, status: 'failed', error: 'Computed script error 77' }
    expect(claudeWorkflowSnapshot(value, launch)).toEqual({ status: 'failed', error: value.error })
    expect(() => claudeWorkflowSnapshot({ ...value, taskId: 'other' }, launch)).toThrow('identity')
    expect(() => claudeWorkflowSnapshot({ ...value, runId: 'other' }, launch)).toThrow('identity')
    expect(() => claudeWorkflowSnapshot({ ...value, status: 'running' }, launch)).toThrow('identity')
    expect(() => claudeWorkflowSnapshot({ ...value, error: '' }, launch)).toThrow('native error')
  })
})

describe('claudeWorkflowOutputFile', () => {
  it('keeps the captured failed task placeholder distinct from completed output JSON', () => {
    expect(claudeWorkflowOutputFile('', 'failed')).toEqual({})
    expect(claudeWorkflowOutputFile('Native diagnostic text', 'failed')).toEqual({})
    expect(() => claudeWorkflowOutputFile('', 'completed')).toThrow('completed')
    expect(claudeWorkflowOutputFile('{"result":"MARKER42"}', 'completed')).toEqual({ result: 'MARKER42' })
    expect(() => claudeWorkflowOutputFile('{', 'completed')).toThrow(SyntaxError)
  })
})

describe('claudeWorkflowOutput', () => {
  it.each(['', 0, false, null])('preserves the native completed return value: %j', (result) => {
    expect(claudeWorkflowOutput({ result })).toEqual({ result })
  })

  it('keeps an absent value distinct and rejects a nonobject full tool output', () => {
    expect(claudeWorkflowOutput({})).toEqual({})
    expect(() => claudeWorkflowOutput([])).toThrow('native JSON object')
  })
})
