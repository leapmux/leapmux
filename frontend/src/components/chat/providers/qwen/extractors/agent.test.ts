import { describe, expect, it } from 'vitest'
import { acpToolFacts } from '../../acp/extractors/toolCall'
import { acpTextContent } from '../../acp/testUtils'
import { qwenAgentRequest, qwenAgentRun, qwenWorkflowRequest, qwenWorkflowRun } from './agent'

/** The facts of one finished Qwen call, as the shared reader builds them from its frame. */
function finished(toolName: string, frame: Record<string, unknown>) {
  return acpToolFacts({ sessionUpdate: 'tool_call_update', toolCallId: 'call', kind: 'other', status: 'completed', _meta: { toolName }, ...frame })
}

const REQUEST = { description: 'Child probe', prompt: 'List', registryKey: 'call' }

describe('qwenAgentRequest', () => {
  it('reads the launch and keys the run by the call that launched it', () => {
    expect(qwenAgentRequest({ description: 'Child probe', prompt: 'List the files', subagent_type: 'general-purpose', run_in_background: true }, 'call-1')).toEqual({
      description: 'Child probe',
      prompt: 'List the files',
      agentType: 'general-purpose',
      metadata: [{ label: 'Background', value: 'Yes' }],
      registryKey: 'call-1',
    })
  })

  // A launch with no arguments still draws a card, and an absent call id is no key.
  it('states a generic description and omits what the launch does not state', () => {
    expect(qwenAgentRequest({}, '')).toEqual({ description: 'Subagent', prompt: '' })
  })

  it('reads only a boolean true as a background launch', () => {
    expect(qwenAgentRequest({ run_in_background: 'true' }, 'c')).not.toHaveProperty('metadata')
    expect(qwenAgentRequest({ run_in_background: false }, 'c')).not.toHaveProperty('metadata')
  })
})

describe('qwenAgentRun', () => {
  const record = (fields: Record<string, unknown>) => ({ type: 'task_execution', subagentName: 'general-purpose', ...fields })

  // The call's own status wins over the record: a stopped or failed call gave no
  // report, whatever the record claims.
  it('keeps the outcome of a call that the reader stopped or that failed', () => {
    const stopped = finished('agent', { status: 'cancelled', content: acpTextContent('stopped early'), rawOutput: record({ status: 'completed', result: 'report' }) })
    expect(qwenAgentRun(stopped, REQUEST)).toEqual({ description: 'Child probe', registryKey: 'call', agentId: '', outcome: 'stopped', metadata: [], body: 'stopped early' })
    const failed = finished('agent', { status: 'failed', content: acpTextContent('crashed'), rawOutput: record({ status: 'completed', result: 'report' }) })
    expect(qwenAgentRun(failed, REQUEST)).toMatchObject({ outcome: 'failed', body: 'crashed' })
  })

  it('claims no state for a call that states no run record', () => {
    expect(qwenAgentRun(finished('agent', { content: acpTextContent('words') }), REQUEST)).toMatchObject({ outcome: 'unknown', agentId: '', metadata: [], body: 'words' })
    expect(qwenAgentRun(finished('agent', { content: acpTextContent('words'), rawOutput: { type: 'shell_result' } }), REQUEST)).toMatchObject({ outcome: 'unknown' })
  })

  it.each([
    ['completed', 'completed'],
    ['failed', 'failed'],
    ['cancelled', 'stopped'],
    ['a_later_word', 'unknown'],
    ['', 'unknown'],
  ])('reads the run status %j as %j', (status, outcome) => {
    expect(qwenAgentRun(finished('agent', { content: acpTextContent('x'), rawOutput: record({ status, result: 'r' }) }), REQUEST).outcome).toBe(outcome)
  })

  it('reads a running run as one that goes on in the background', () => {
    const run = qwenAgentRun(finished('agent', { content: acpTextContent('Launched.\ntask_id: general-purpose-call_7\nWorking.'), rawOutput: record({ status: 'running' }) }), REQUEST)
    expect(run).toMatchObject({ agentId: 'general-purpose-call_7', outcome: 'running', statusLabel: 'running in the background', body: '' })
  })

  // The id is the one fact a later message or stop refers to. A launch whose prose
  // states none claims no id and no empty metadata line.
  it('states no task id for a background launch whose prose states none', () => {
    const run = qwenAgentRun(finished('agent', { content: acpTextContent('Background agent launched.'), rawOutput: record({ status: 'background' }) }), REQUEST)
    expect(run).toMatchObject({ agentId: '', outcome: 'running', metadata: [{ label: 'Type', value: 'general-purpose' }] })
  })

  // A count of zero is a count. Only an absent count drops its line.
  it('keeps a zero count and drops an absent one', () => {
    const run = qwenAgentRun(finished('agent', {
      content: acpTextContent('text'),
      rawOutput: record({ status: 'completed', result: 'report', terminateReason: 'GOAL', executionSummary: { rounds: 0, totalDurationMs: 0, totalTokens: 12 } }),
    }), REQUEST)
    expect(run.metadata).toEqual([
      { label: 'Type', value: 'general-purpose' },
      { label: 'Rounds', value: '0' },
      { label: 'Tokens', value: '12' },
      { label: 'Duration', value: '0.0s' },
    ])
  })

  it('draws the words of the call when the record states no report', () => {
    expect(qwenAgentRun(finished('agent', { content: acpTextContent('fallback words'), rawOutput: record({ status: 'completed', result: '' }) }), REQUEST).body).toBe('fallback words')
  })
})

describe('qwenWorkflowRequest', () => {
  it('describes a saved workflow by its name, with the run it resumes and its arguments', () => {
    expect(qwenWorkflowRequest({ name: 'review', resumeFromRunId: 'wf_0', args: 'depth=2', script: 'await agent("x")' })).toEqual({
      description: 'review',
      prompt: 'await agent("x")',
      promptLabel: 'Script',
      promptFormat: 'pre',
      metadata: [
        { label: 'Saved workflow', value: 'review' },
        { label: 'Previous run', value: 'wf_0' },
        { label: 'Arguments', value: 'depth=2' },
      ],
    })
  })

  it('describes a script file by its path, and an inline script by a generic word', () => {
    expect(qwenWorkflowRequest({ scriptPath: '/w/review.js' }).description).toBe('/w/review.js')
    expect(qwenWorkflowRequest({ script: 'x', args: null })).toEqual({ description: 'Run workflow', prompt: 'x', promptLabel: 'Script', promptFormat: 'pre' })
  })
})

describe('qwenWorkflowRun', () => {
  const REQUEST_RUN = { description: 'review', prompt: '' }

  it('keeps the outcome of a workflow call that the reader stopped or that failed', () => {
    const stopped = finished('workflow', { status: 'cancelled', content: acpTextContent('stopped'), rawOutput: '{"runId":"wf_1","result":"done"}' })
    expect(qwenWorkflowRun(stopped, REQUEST_RUN)).toEqual({ description: 'review', agentId: '', outcome: 'stopped', metadata: [], body: 'stopped' })
    const failed = finished('workflow', { status: 'failed', content: acpTextContent('broke'), rawOutput: '{"runId":"wf_1","result":"done"}' })
    expect(qwenWorkflowRun(failed, REQUEST_RUN)).toMatchObject({ outcome: 'failed', body: 'broke' })
  })

  it('reads the record with or without a fence and a language tag', () => {
    for (const rawOutput of ['```json\n{"runId":"wf_1","result":"done"}\n```', '```\n{"runId":"wf_1","result":"done"}\n```', '{"runId":"wf_1","result":"done"}']) {
      expect(qwenWorkflowRun(finished('workflow', { content: acpTextContent('text'), rawOutput }), REQUEST_RUN), rawOutput).toMatchObject({ agentId: 'wf_1', body: 'done' })
    }
  })

  // The record is wire data. A record that is no JSON object states no run, and the
  // card draws the words of the call.
  it.each([
    ['an array', '["wf_1"]'],
    ['text', 'not json'],
    ['an object that is no string', { runId: 'wf_1' }],
  ])('draws the words of the call for a record that is %s', (_shape, rawOutput) => {
    expect(qwenWorkflowRun(finished('workflow', { content: acpTextContent('words'), rawOutput }), REQUEST_RUN)).toMatchObject({ agentId: '', outcome: 'completed', metadata: [], body: 'words' })
  })

  it('draws the words of the call when the record returns no result', () => {
    expect(qwenWorkflowRun(finished('workflow', { content: acpTextContent('words'), rawOutput: '{"runId":"wf_1","result":null}' }), REQUEST_RUN).body).toBe('words')
  })

  it('states only the phases that are words, and no token line without a count', () => {
    const run = qwenWorkflowRun(finished('workflow', { content: acpTextContent('t'), rawOutput: '{"runId":"wf_1","phases":["a",2,null,"b"],"tokens":"many","result":"ok"}' }), REQUEST_RUN)
    expect(run.metadata).toEqual([{ label: 'Run ID', value: 'wf_1' }, { label: 'Phases', value: 'a, b' }])
  })
})
