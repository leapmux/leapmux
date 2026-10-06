import { describe, expect, it } from 'vitest'
import { ZCODE_EVENT } from '../../../src/generated/contracts/zcode-protocol'
import { zcodeStoredWorkflowCompletion, zcodeWorkflowCompletion, zcodeWorkflowLaunch } from './codeExecution'

const runId = 'dwfrun-11111111-1111-4111-8111-111111111111'
function output(status = 'completed', value = 'answer42') {
  return `<summary>Native result.</summary>\n<run_id>${runId}</run_id>\n<status>${status}</status>\n<owned_by_this_session>true</owned_by_this_session>\n${status === 'completed' ? `<result>\n${value}\n</result>` : `<error code="DriverError">${value}</error>`}`
}
function frame(content = output(), changes: Record<string, unknown> = {}) {
  return { sessionId: 'native-session', type: ZCODE_EVENT.ToolUpdated, payload: { kind: 'result', toolCallId: 'native-read', result: { success: true, content } }, ...changes }
}

describe('zcodeWorkflowLaunch', () => {
  it('reads the actual background run ID', () => {
    expect(zcodeWorkflowLaunch(`The workflow script compiled cleanly and the run started in the background with ID: ${runId}. It is still running.`)).toEqual({ runId })
  })
  it.each(['', 'The workflow script compiled cleanly.', 'NOTE: The workflow was NOT executed.', 'The workflow started in the background with ID: other.'])('rejects a compile check or absent launch', (text) => {
    expect(() => zcodeWorkflowLaunch(text)).toThrow('actual workflow launch')
  })
})

describe('zcodeWorkflowCompletion', () => {
  it.each(['answer42', 'null', '0', 'false', '', '  actual text  ', '&amp;lt;literal&amp;gt;'])('retains the complete native value %j', (value) => {
    expect(zcodeWorkflowCompletion(output('completed', value), runId)).toEqual({ runId, status: 'completed', text: value })
  })
  it('reads the native failure without a successful outer tool error flag', () => {
    expect(zcodeWorkflowCompletion(output('errored', 'computed77'), runId)).toEqual({ runId, status: 'failed', text: 'computed77' })
  })
  it('decodes an escaped error once', () => {
    expect(zcodeWorkflowCompletion(output('errored', '&amp;lt;script&amp;gt; &quot;computed77&quot;'), runId).text).toBe('&lt;script&gt; "computed77"')
  })
  it('retains an empty native execution error', () => {
    expect(zcodeWorkflowCompletion(output('errored', ''), runId).text).toBe('')
  })
  it.each([
    [output().replace('<owned_by_this_session>true', '<owned_by_this_session>false'), 'The native ZCode result belongs to another run or session.'],
    [output().replace(`<run_id>${runId}`, '<run_id>other'), 'The native ZCode result belongs to another run or session.'],
    [output().replace('<status>completed', '<status>running'), 'The native ZCode script has no completed result or execution error.'],
    [output().replace('<result>\nanswer42\n</result>', ''), 'The native ZCode script has no exact final output.'],
    [`${output()}<run_id>other</run_id>`, 'The native ZCode workflow repeats its run_id field.'],
    [`${output()}<status>errored</status>`, 'The native ZCode workflow repeats its status field.'],
    [`${output()}<error code="DriverError">contradictory</error>`, 'The native ZCode script has no exact final output.'],
    [output('errored', 'failure').replace('<error code="DriverError">failure</error>', ''), 'The native ZCode script has no exact execution error.'],
  ])('rejects an incomplete, repeated, or foreign final result', (text, error) => {
    expect(() => zcodeWorkflowCompletion(text, runId)).toThrow(error)
  })
})

describe('zcodeStoredWorkflowCompletion', () => {
  it('reads the exact persisted card after reload', () => {
    expect(zcodeStoredWorkflowCompletion([frame()], 'native-session', 'native-read', runId)).toEqual({ runId, status: 'completed', text: 'answer42' })
  })
  it('rejects another session, call, and repeated result', () => {
    expect(() => zcodeStoredWorkflowCompletion([frame(output(), { sessionId: 'other' })], 'native-session', 'native-read', runId)).toThrow('another session')
    expect(() => zcodeStoredWorkflowCompletion([frame()], 'native-session', 'other', runId)).toThrow('one exact persisted result')
    expect(() => zcodeStoredWorkflowCompletion([frame(), frame()], 'native-session', 'native-read', runId)).toThrow('one exact persisted result')
  })
  it('rejects a truncated native read', () => {
    expect(() => zcodeStoredWorkflowCompletion([frame(output(), { payload: { kind: 'result', toolCallId: 'native-read', result: { success: true, content: output(), truncated: true } } })], 'native-session', 'native-read', runId)).toThrow('incomplete')
  })
})
