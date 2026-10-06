import { describe, expect, it } from 'vitest'
import { qoderWorkflowDiagnosticJson, qoderWorkflowLaunch, qoderWorkflowModelOutcome, qoderWorkflowOutput } from './codeExecution'

const launch = { sessionId: 'session', callId: 'call', taskId: 'wf-task', runId: 'wf_run', transcriptDir: '/native/run', scriptPath: '/native/script.js' }

describe('qoderWorkflowDiagnosticJson', () => {
  it('preserves exact sequence and revision integers without changing ordinary values', () => {
    expect(JSON.parse(qoderWorkflowDiagnosticJson({ seq: 0n, revision: 9007199254740993n, activeTurn: false, count: 0, text: '' }))).toEqual({
      seq: '0',
      revision: '9007199254740993',
      activeTurn: false,
      count: 0,
      text: '',
    })
    expect(() => qoderWorkflowDiagnosticJson(undefined)).toThrow('serialized')
  })
})

function modelNotification(status = 'completed', result = 'MARKER42'): unknown {
  return { messages: [{ role: 'user', content: [
    '<task-notification>',
    '<task-id>wf-task</task-id>',
    '<tool-use-id>call</tool-use-id>',
    '<output-file>/native/run/output.json</output-file>',
    `<status>${status}</status>`,
    '<summary>Native script error MARKER77</summary>',
    ...(status === 'completed' ? [`<result>${result}</result>`] : []),
    '</task-notification>',
  ].join('\n') }] }
}

describe('qoderWorkflowModelOutcome', () => {
  it('retains the captured native raw string result instead of parsing it as JSON', () => {
    expect(qoderWorkflowModelOutcome([modelNotification('completed', 'NATIVEWORKFLOW42')], launch)).toEqual({
      status: 'completed',
      summary: 'Native script error MARKER77',
      outputFile: '/native/run/output.json',
      result: 'NATIVEWORKFLOW42',
    })
  })

  it.each(['', '0', 'false', 'null', '{"value":0}'])('preserves native result text without inferring its JSON type: %j', (result) => {
    expect(qoderWorkflowModelOutcome([modelNotification('completed', result)], launch)?.result).toBe(result)
  })

  it('reads Qoder string content only for the exact native task and call', () => {
    expect(qoderWorkflowModelOutcome([], launch)).toBeUndefined()
    expect(qoderWorkflowModelOutcome([modelNotification()], launch)).toEqual({ status: 'completed', summary: 'Native script error MARKER77', outputFile: '/native/run/output.json', result: 'MARKER42' })
    expect(qoderWorkflowModelOutcome([modelNotification()], { ...launch, taskId: 'wf-other' })).toBeUndefined()
    expect(qoderWorkflowModelOutcome([modelNotification()], { ...launch, callId: 'other' })).toBeUndefined()
    const text: unknown = JSON.parse(JSON.stringify(modelNotification()).replace('"role":"user"', '"role":"assistant"'))
    expect(qoderWorkflowModelOutcome([text], launch)).toBeUndefined()
  })

  it('preserves native failure and refuses conflicting repeated outcomes', () => {
    expect(qoderWorkflowModelOutcome([modelNotification('failed')], launch)).toEqual({ status: 'failed', summary: 'Native script error MARKER77', outputFile: '/native/run/output.json' })
    expect(qoderWorkflowModelOutcome([modelNotification(), modelNotification()], launch)?.result).toBe('MARKER42')
    expect(() => qoderWorkflowModelOutcome([modelNotification(), modelNotification('failed')], launch)).toThrow('different final outcome')
  })

  it('rejects a running notification and preserves JSON-looking result text', () => {
    expect(() => qoderWorkflowModelOutcome([modelNotification('running')], launch)).toThrow('final native outcome')
    expect(qoderWorkflowModelOutcome([modelNotification('completed', '{')], launch)?.result).toBe('{')
  })
})

describe('qoderWorkflowLaunch', () => {
  it('decodes the native string payload only for the original model call', () => {
    const value = { session_id: 'session', message: { content: [{ type: 'tool_result', tool_use_id: 'call' }] }, tool_use_result: { payload: JSON.stringify({ status: 'async_launched', ...launch }) } }
    expect(qoderWorkflowLaunch(value, 'call')).toEqual(launch)
    expect(qoderWorkflowLaunch(value, 'other')).toBeUndefined()
  })

  it('refuses incomplete native identity and malformed payload JSON', () => {
    const value = { session_id: 'session', message: { content: [{ type: 'tool_result', tool_use_id: 'call' }] }, tool_use_result: { payload: JSON.stringify({ status: 'async_launched', ...launch }) } }
    expect(qoderWorkflowLaunch({ ...value, session_id: '' }, 'call')).toBeUndefined()
    expect(qoderWorkflowLaunch({ ...value, tool_use_result: { payload: JSON.stringify({ status: 'async_launched', ...launch, taskId: 'wf-', runId: 'wf_' }) } }, 'call')).toBeUndefined()
    expect(() => qoderWorkflowLaunch({ ...value, tool_use_result: { payload: '{' } }, 'call')).toThrow(SyntaxError)
  })
})

describe('qoderWorkflowOutput', () => {
  it.each(['', 0, false, null])('preserves a completed native return value: %j', (result) => {
    expect(qoderWorkflowOutput({ ...launch, status: 'completed', result }, launch)).toEqual({ status: 'completed', result })
  })

  it('keeps explicit native failure and refuses another run', () => {
    expect(qoderWorkflowOutput({ ...launch, status: 'failed', error: 'computed failure 77' }, launch)).toEqual({ status: 'failed', error: 'computed failure 77' })
    expect(() => qoderWorkflowOutput({ ...launch, runId: 'wf_other', status: 'completed', result: 42 }, launch)).toThrow('identity')
    expect(() => qoderWorkflowOutput({ ...launch, taskId: 'wf-other', status: 'completed', result: 42 }, launch)).toThrow('identity')
    expect(() => qoderWorkflowOutput({ ...launch, status: 'running' }, launch)).toThrow('identity')
    expect(() => qoderWorkflowOutput({ ...launch, status: 'failed' }, launch)).toThrow('native error')
  })

  it('rejects malformed canonical JSON rather than accepting raw notification text as a full tool output', () => {
    expect(() => qoderWorkflowOutput('{', launch)).toThrow('identity')
    expect(() => qoderWorkflowOutput({ ...launch, status: 'failed', error: '' }, launch)).toThrow('native error')
  })
})
