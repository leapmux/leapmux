import type { MockModelRequestRecord, MockModelRule } from '../helpers/mockModelScript'
import { describe, expect, it } from 'vitest'
import { matchesRequest } from '../helpers/mockModelScript'
import { piChildLaunch, piChildNoticeRule, piWorkflowNoticeRule } from './childNoticeRule'

const options = { name: 'the actual Pi child completed', spawnCallId: 'native-pi-spawn', description: 'Read "A&B<file>"', report: 'REPORT & <native>\nThe report ends here.', reply: 'The actual Pi report arrived.' }
const launch = { spawnCallId: options.spawnCallId, description: options.description, childId: '12345678-abcd-123', outputFile: '/private/A&B/12345678-abcd-123.output' }
const xml = (text: string) => text.replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;')

function receipt(text = `Agent started in background.\nAgent ID: ${launch.childId}\nOutput file: ${launch.outputFile}\n`, callName = 'Agent'): MockModelRequestRecord {
  return {
    protocol: 'openai-chat-completions',
    path: '/v1/chat/completions',
    body: { messages: [{ role: 'assistant', tool_calls: [{ id: options.spawnCallId, function: { name: callName, arguments: JSON.stringify({ description: options.description, prompt: 'Perform the actual child task.', subagent_type: 'general-purpose' }) } }] }, { role: 'tool', tool_call_id: options.spawnCallId, content: text }] },
  }
}

function notice() {
  return `<task-notification>\n<task-id>${launch.childId}</task-id>\n<tool-use-id>${options.spawnCallId}</tool-use-id>\n<output-file>${xml(launch.outputFile)}</output-file>\n<status>Done</status>\n<summary>Agent "${xml(options.description)}" completed</summary>\n<result>${xml(options.report)}</result>\n<usage><total_tokens>0</total_tokens><tool_uses>1</tool_uses><duration_ms>0</duration_ms></usage>\n</task-notification>\nFull transcript available at: ${launch.outputFile}`
}

function accepts(rule: MockModelRule, text: string, lastRole = 'user') {
  return matchesRequest(rule.when, { protocol: 'openai-chat-completions', systemText: '', userText: text, body: { messages: [{ role: 'user', content: text }, ...(lastRole === 'user' ? [] : [{ role: lastRole, content: 'A later native message.' }])] } })
}

describe('piChildLaunch', () => {
  it('reads the native child ID and output path from the actual Agent result', () => {
    const request = receipt()
    expect(piChildLaunch(request, options.spawnCallId)).toEqual(launch)
    expect(request.body).toEqual(receipt().body)
  })

  it('retains a queued launch without an optional output path', () => {
    expect(piChildLaunch(receipt(`Agent queued in background.\nAgent ID: ${launch.childId}\n`), options.spawnCallId)).toEqual({ spawnCallId: options.spawnCallId, description: options.description, childId: launch.childId })
  })

  it('ignores a request before its actual tool result arrives', () => {
    expect(piChildLaunch({ protocol: 'openai-chat-completions', path: '/', body: { messages: [{ role: 'user', content: 'Agent ID: guessed-id' }] } }, options.spawnCallId)).toBeNull()
  })

  it.each(['Agent failed.\n', `Agent started in background.\nAgent ID: ${launch.childId}\nAgent ID: ${launch.childId}\n`, 'Agent started in background.\nAgent ID: foreign-id\n', `Agent started in background.\nAgent ID: ${launch.childId}\nOutput file: /first\nOutput file: /second\n`])('rejects an invalid native launch receipt: %j', (text) => {
    expect(() => piChildLaunch(receipt(text), options.spawnCallId)).toThrow(/Pi child receipt/)
  })

  it('rejects a result whose call ID belongs to another native tool', () => {
    expect(() => piChildLaunch(receipt(undefined, 'bash'), options.spawnCallId)).toThrow('unique actual Agent call')
  })
})

describe('piChildNoticeRule', () => {
  it('matches the complete correlated native envelope with zero metrics and escaped report text', () => {
    const rule = piChildNoticeRule(launch, options)
    expect(accepts(rule, notice())).toBe(true)
    expect(rule.once).toBeUndefined()
    expect(accepts(rule, notice())).toBe(true)
  })

  it('accepts native context and cost metrics in their source order', () => {
    const text = notice().replace('<duration_ms>0', '<context_percent>0</context_percent><compactions>2</compactions><estimated_cost_usd>0.01</estimated_cost_usd><duration_ms>0')
    expect(accepts(piChildNoticeRule(launch, { ...options, once: true }), text)).toBe(true)
  })

  it.each([
    ['the native task ID', launch.childId, '87654321-abcd-123'],
    ['the actual call ID', options.spawnCallId, 'foreign-spawn'],
    ['the summary', xml(options.description), 'A foreign task'],
    ['the actual report', xml(options.report), 'A foreign report'],
    ['the completion status', '<status>Done</status>', '<status>Stopped</status>'],
    ['the output footer', `Full transcript available at: ${launch.outputFile}`, 'Full transcript available at: /foreign/output'],
    ['a required metric', '<tool_uses>1</tool_uses>', ''],
  ])('rejects a notice with the wrong %s', (_field, from, to) => {
    expect(accepts(piChildNoticeRule(launch, options), notice().replaceAll(from, to))).toBe(false)
  })

  it.each(['prefix', 'quoted', 'nested', 'duplicated', 'workflow'])('rejects an unrelated or malformed %s envelope', (kind) => {
    const text = kind === 'prefix'
      ? `An unrelated user message.\n${notice()}`
      : kind === 'quoted'
        ? `The literal "${notice()}"`
        : kind === 'nested'
          ? notice().replace('<result>', '<result><task-notification>')
          : kind === 'duplicated'
            ? `${notice()}\n${notice()}`
            : notice().replace('<summary>Agent ', '<summary>Workflow ')
    expect(accepts(piChildNoticeRule(launch, options), text)).toBe(false)
  })

  it('rejects a prior user notice after the model appends another native message', () => {
    expect(accepts(piChildNoticeRule(launch, options), notice(), 'assistant')).toBe(false)
    expect(accepts(piChildNoticeRule(launch, options), notice(), 'tool')).toBe(false)
  })

  it('rejects a guessed call or description instead of the actual launch record', () => {
    expect(() => piChildNoticeRule(launch, { ...options, spawnCallId: 'foreign' })).toThrow('actual Agent call')
    expect(() => piChildNoticeRule(launch, { ...options, description: 'Foreign child' })).toThrow('actual Agent call')
    expect(() => piChildNoticeRule({ ...launch, childId: '' }, options)).toThrow('actual native child ID')
  })
})

describe('piWorkflowNoticeRule', () => {
  it('accepts the captured native workflow total of ten raw progress records', () => {
    const captured = '<task-notification>\n<task-id>wf_d03cd0d8ba09</task-id>\n<tool-use-id>native-pi-workflow</tool-use-id>\n<script>/var/folders/ph/0qx1sm5d2w3dmmgckzz91wqr0000gn/T/pi-subagents-501/Users-trustin-Workspaces-leapmux-.tmp-e2e-28KS3u-pi-e2e-wd-kp9RnT/01a0f667-32dd-7716-8e91-2b9df1949d01/tasks/wf_d03cd0d8ba09.workflow.js</script>\n<status>Done</status>\n<summary>Workflow "Native two-stage workflow" completed — 2/10 agents</summary>\n<result>{\n  "first": "ACTUAL_FIRST_WORKFLOW_ANSWER_c6c2155c73d04bc697a8b1ae274045c7",\n  "second": "ACTUAL_SECOND_WORKFLOW_ANSWER_c6c2155c73d04bc697a8b1ae274045c7"\n}</result>\n<usage><total_tokens>4</total_tokens><tool_uses>0</tool_uses><duration_ms>933</duration_ms></usage>\n</task-notification>'
    const rule = piWorkflowNoticeRule({ name: 'the captured native workflow completed', taskId: 'wf_d03cd0d8ba09', callId: 'native-pi-workflow', workflowName: 'Native two-stage workflow', scriptPath: '/var/folders/ph/0qx1sm5d2w3dmmgckzz91wqr0000gn/T/pi-subagents-501/Users-trustin-Workspaces-leapmux-.tmp-e2e-28KS3u-pi-e2e-wd-kp9RnT/01a0f667-32dd-7716-8e91-2b9df1949d01/tasks/wf_d03cd0d8ba09.workflow.js', reports: ['ACTUAL_FIRST_WORKFLOW_ANSWER_c6c2155c73d04bc697a8b1ae274045c7', 'ACTUAL_SECOND_WORKFLOW_ANSWER_c6c2155c73d04bc697a8b1ae274045c7'], reply: 'The captured native workflow result arrived.' })
    expect(accepts(rule, captured)).toBe(true)
    expect(accepts(rule, captured.replace('completed — 2/10 agents', 'completed — 1/10 agents'))).toBe(false)
  })
  const workflow = { name: 'the actual workflow completes', taskId: 'wf_123-native', callId: 'actual-workflow', workflowName: 'Native two-stage workflow', scriptPath: '/private/workflow.js', reports: ['FIRST_NATIVE_REPORT', 'SECOND_NATIVE_REPORT'], reply: 'The workflow completed.' }
  const text = `<task-notification>\n<task-id>${workflow.taskId}</task-id>\n<tool-use-id>${workflow.callId}</tool-use-id>\n<script>${workflow.scriptPath}</script>\n<status>Done</status>\n<summary>Workflow "${workflow.workflowName}" completed — 2/2 agents</summary>\n<result>{"first":"FIRST_NATIVE_REPORT","second":"SECOND_NATIVE_REPORT"}</result>\n<usage><total_tokens>0</total_tokens><tool_uses>0</tool_uses><duration_ms>0</duration_ms></usage>\n</task-notification>`

  it('requires the actual workflow identity and both native stage reports', () => {
    const rule = piWorkflowNoticeRule(workflow)
    expect(accepts(rule, text)).toBe(true)
    expect(accepts(rule, text.replace('FIRST_NATIVE_REPORT', 'FOREIGN'))).toBe(false)
    expect(accepts(rule, text.replace('SECOND_NATIVE_REPORT', 'FOREIGN'))).toBe(false)
    expect(accepts(rule, text.replace(workflow.taskId, 'wf_foreign'))).toBe(false)
    expect(accepts(rule, text.replace(workflow.callId, 'foreign-call'))).toBe(false)
    expect(accepts(rule, text.replace('2/2 agents', '1/2 agents'))).toBe(false)
  })

  it('rejects a child envelope and an empty report list', () => {
    expect(accepts(piWorkflowNoticeRule(workflow), notice())).toBe(false)
    expect(() => piWorkflowNoticeRule({ ...workflow, reports: [] })).toThrow('actual task ID and reports')
  })
})
