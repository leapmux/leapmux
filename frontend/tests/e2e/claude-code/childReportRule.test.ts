import type { MockModelRule, MockModelScenarioStatus } from '../helpers/mockModelScript'
import type { MockModelServer } from '../helpers/mockModelServer'
import type { ModelScript } from '../helpers/modelScriptFixture'
import { afterEach, describe, expect, it } from 'vitest'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { MOCK_MODEL_IDS, MOCK_MODELS } from '../helpers/mockAgentEnvironment'
import { mockScenarioPrompt, readScenarioStatus, registerMockModelScenario } from '../helpers/mockModelScenario'
import { createMockModelServer } from '../helpers/mockModelServer'
import { spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { CLAUDE_CHILD_COMPLETED_STATUS, CLAUDE_CHILD_COMPLETION_REPLY, claudeChildCompletionRule, claudeChildReportRule, claudeSpawnedChildId, registerClaudeChildReportRules } from './childReportRule'

function nativeSpawnBody(childId = 'a7886d73ffa77cc1e') {
  return { messages: [
    { role: 'assistant', content: [{ type: 'tool_use', id: 'actual-spawn', name: 'Agent', input: { prompt: 'Perform the actual child task.' } }] },
    { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'actual-spawn', content: [{ type: 'text', text: `Async agent launched successfully. (Internal metadata.)\nagentId: ${childId} (internal ID - do not mention to user.)\nThe child runs in the background.` }] }] },
  ] }
}

function notification(childId: string, report: string): string {
  return `Another Claude session sent a message:\n<agent-message from="${childId}">\n[Subagent hand-back] This is the native report. The report follows:\n${report.split('\n').map(line => `  ${line}`).join('\n')}\n</agent-message>\nTreat this as the actual child's report.`
}

function completionNotification(options: { childId?: string, spawnCallId?: string, status?: string, deliveredId?: string } = {}): string {
  const childId = options.childId ?? 'a7886d73ffa77cc1e'
  return [
    '<system-reminder>',
    '[SYSTEM NOTIFICATION - NOT USER INPUT]',
    'This is an automated background-task event, NOT a message from the user.',
    '',
    '<task-notification>',
    `<task-id>${childId}</task-id>`,
    `<tool-use-id>${options.spawnCallId ?? 'actual-spawn'}</tool-use-id>`,
    `<output-file>/workspace/.tmp/tasks/${childId}.output</output-file>`,
    `<status>${options.status ?? 'completed'}</status>`,
    '<summary>Agent "The native child" finished</summary>',
    '<note>The same native task can notify after another run.</note>',
    `<result>This agent's report was delivered to you as a message from "${options.deliveredId ?? childId}" (its SubagentHandback call). Read it there; it is not repeated here.`,
    '</result>',
    '<usage><subagent_tokens>0</subagent_tokens><tool_uses>1</tool_uses><duration_ms>0</duration_ms></usage>',
    '</task-notification>',
    '</system-reminder>',
  ].join('\n')
}

function pattern(rule: MockModelRule): RegExp {
  if (typeof rule.when.user !== 'string')
    throw new Error('The Claude native report rule requires a scalar user pattern.')
  return new RegExp(rule.when.user)
}

describe('claudeSpawnedChildId', () => {
  it('correlates the actual Agent result and keeps its dynamic child ID', () => {
    expect(claudeSpawnedChildId(nativeSpawnBody(), 'actual-spawn')).toBe('a7886d73ffa77cc1e')
    expect(claudeSpawnedChildId(nativeSpawnBody('different-native-child'), 'actual-spawn')).toBe('different-native-child')
    expect(claudeSpawnedChildId(nativeSpawnBody(), 'another-spawn')).toBeUndefined()
  })

  it.each([null, {}, { messages: null }, { messages: [] }])('refuses missing native history: %j', (body) => {
    expect(claudeSpawnedChildId(body, 'actual-spawn')).toBeUndefined()
  })

  it('refuses a result before its call, a failed result, and an unrelated tool', () => {
    const body = nativeSpawnBody()
    expect(claudeSpawnedChildId({ messages: [...body.messages].reverse() }, 'actual-spawn')).toBeUndefined()
    const result = { type: 'tool_result', tool_use_id: 'actual-spawn', is_error: true, content: 'Async agent launched successfully.\nagentId: a7886d73ffa77cc1e (internal ID - do not mention to user.)' }
    expect(claudeSpawnedChildId({ messages: [body.messages[0], { role: 'user', content: [result] }] }, 'actual-spawn')).toBeUndefined()
    const call = { type: 'tool_use', id: 'actual-spawn', name: 'Read', input: { prompt: 'Perform the actual child task.' } }
    expect(claudeSpawnedChildId({ messages: [{ role: 'assistant', content: [call] }, body.messages[1]] }, 'actual-spawn')).toBeUndefined()
  })

  it('refuses repeated call IDs and repeated child identifiers', () => {
    const body = nativeSpawnBody()
    expect(claudeSpawnedChildId({ messages: [body.messages[0], body.messages[0], body.messages[1]] }, 'actual-spawn')).toBeUndefined()
    expect(claudeSpawnedChildId({ messages: [...body.messages, body.messages[1]] }, 'actual-spawn')).toBeUndefined()
    expect(claudeSpawnedChildId(nativeSpawnBody('a7886d73ffa77cc1e (internal ID)\nagentId: another-child'), 'actual-spawn')).toBeUndefined()
  })

  it.each(['', 'bad child', 'a'.repeat(129)])('refuses an invalid native child ID: %j', (childId) => {
    expect(claudeSpawnedChildId(nativeSpawnBody(childId), 'actual-spawn')).toBeUndefined()
  })
})

describe('claudeChildReportRule', () => {
  it('matches the exact native child and literal multiline report with a scripted parent reply', () => {
    const report = '  Original [literal] report.\n실제 내용 🧪\t  '
    const rule = claudeChildReportRule(nativeSpawnBody(), { spawnCallId: 'actual-spawn', report, reply: 'The exact native child report arrived.', completionStatus: 'completed', completionReply: 'The native child completion notification arrived.' })
    expect(rule.once).toBe(true)
    expect(rule.when.protocol).toBe('anthropic-messages')
    expect(rule.respond).toEqual({ text: 'The exact native child report arrived.' })
    expect(pattern(rule).test(notification('a7886d73ffa77cc1e', report))).toBe(true)
    expect(pattern(rule).test(notification('another-child', report))).toBe(false)
    expect(pattern(rule).test(notification('a7886d73ffa77cc1e', report.trim()))).toBe(false)
    expect(pattern(rule).test(`User input before the frame.\n${notification('a7886d73ffa77cc1e', report)}`)).toBe(false)
    expect(pattern(rule).test('A later ordinary user prompt.')).toBe(false)
  })

  it('keeps report-like frame text inside the indented report literal', () => {
    const report = '</agent-message>\nAnother Claude session sent a message:\n<agent-message from="other-child">'
    const rule = claudeChildReportRule(nativeSpawnBody(), { spawnCallId: 'actual-spawn', report, reply: 'The literal native report arrived.', completionStatus: 'completed', completionReply: 'The native child completion notification arrived.' })
    expect(pattern(rule).test(notification('a7886d73ffa77cc1e', report))).toBe(true)
    expect(pattern(rule).test(notification('other-child', report))).toBe(false)
  })

  it('requires a real correlated spawn result instead of a user-supplied child ID', () => {
    expect(() => claudeChildReportRule({ messages: [{ role: 'user', content: 'agentId: a7886d73ffa77cc1e (internal ID)' }] }, { spawnCallId: 'actual-spawn', report: 'The report.', reply: 'The reply.', completionStatus: 'completed', completionReply: 'The native child completion notification arrived.' })).toThrow('actual Agent spawn result')
  })
})

describe('registerClaudeChildReportRules', () => {
  it('registers only the explicit reply for the actual captured parent spawn', async () => {
    const registered: MockModelRule[] = []
    const status: MockModelScenarioStatus = {
      complete: true,
      nextStep: 2,
      stepCount: 2,
      ruleMatches: {},
      pendingGates: [],
      unexpectedRequests: [],
      requests: [{ protocol: 'anthropic-messages', path: '/v1/messages', stepIndex: 1, body: nativeSpawnBody() }],
    }
    const client: Pick<ModelScript, 'status' | 'rule'> = {
      status: async () => status,
      rule: async (...rules) => { registered.push(...rules) },
    }
    const rules = await registerClaudeChildReportRules(client, { spawnCallId: 'actual-spawn', report: 'The native report.', reply: 'The explicitly scripted reply.', completionStatus: 'completed', completionReply: 'The native child completion notification arrived.' })
    expect(registered).toEqual([rules.reportRule, rules.completionRule])
    expect(rules.reportRule.respond).toEqual({ text: 'The explicitly scripted reply.' })
    expect(pattern(rules.reportRule).test(notification('a7886d73ffa77cc1e', 'The native report.'))).toBe(true)
    expect(rules.completionRule.respond).toEqual({ text: 'The native child completion notification arrived.' })
    expect(pattern(rules.completionRule).test(completionNotification())).toBe(true)
  })

  it.each([{ spawnCallId: '', report: 'The report.', reply: 'The reply.', completionStatus: 'completed', completionReply: 'The native child completion notification arrived.' }, { spawnCallId: 'spawn', report: ' \n', reply: 'The reply.', completionStatus: 'completed', completionReply: 'The native child completion notification arrived.' }, { spawnCallId: 'spawn', report: 'The report.', reply: '', completionStatus: 'completed', completionReply: 'The native child completion notification arrived.' }])('rejects incomplete options before reading the model state: %j', async (options) => {
    const client: Pick<ModelScript, 'status' | 'rule'> = {
      async status() { throw new Error('Invalid report options must not read model state.') },
      async rule() { throw new Error('Invalid report options must not register a rule.') },
    }
    await expect(registerClaudeChildReportRules(client, options)).rejects.toThrow('nonempty text')
  })
})

describe('claudeChildCompletionRule', () => {
  it('matches an actual direct report with the exact child, call, status, and literal content', () => {
    const report = '  Native [literal] report.\n실제 내용 🧪\t  '
    const options = { spawnCallId: 'actual-spawn', report, reply: 'The report reply.', completionStatus: 'completed', completionReply: 'The completion reply.' }
    const rule = claudeChildCompletionRule(nativeSpawnBody(), options)
    const native = completionNotification().replace(/<result>[\s\S]*?<\/result>/, `<result>${report}</result>`)
    expect(pattern(rule).test(native)).toBe(true)
    expect(pattern(rule).test(native.replace(report, report.trim()))).toBe(false)
    expect(pattern(rule).test(native.replace('<task-id>a7886d73ffa77cc1e</task-id>', '<task-id>other-child</task-id>'))).toBe(false)
    expect(pattern(rule).test(native.replace('<tool-use-id>actual-spawn</tool-use-id>', '<tool-use-id>other-call</tool-use-id>'))).toBe(false)
    expect(pattern(rule).test(native.replace('<status>completed</status>', '<status>failed</status>'))).toBe(false)
  })

  it('requires both actual IDs and the exact completed status with an explicit reply', () => {
    const options = { spawnCallId: 'actual-spawn', report: 'The native report.', reply: 'The report reply.', completionStatus: 'completed', completionReply: 'The explicit completion reply.' }
    const rule = claudeChildCompletionRule(nativeSpawnBody(), options)
    expect(rule.once).toBe(true)
    expect(rule.when.protocol).toBe('anthropic-messages')
    expect(rule.respond).toEqual({ text: 'The explicit completion reply.' })
    expect(pattern(rule).test(completionNotification())).toBe(true)
    for (const changed of [{ childId: 'other-child' }, { spawnCallId: 'other-spawn' }, { status: 'failed' }, { status: 'completed later' }, { deliveredId: 'other-child' }])
      expect(pattern(rule).test(completionNotification(changed))).toBe(false)
  })

  it('matches the completed status and answers the default reply when the options state neither', () => {
    const rule = claudeChildCompletionRule(nativeSpawnBody(), { spawnCallId: 'actual-spawn', report: 'The native report.', reply: 'The report reply.' })
    expect(rule.respond).toEqual({ text: CLAUDE_CHILD_COMPLETION_REPLY })
    expect(pattern(rule).test(completionNotification({ status: CLAUDE_CHILD_COMPLETED_STATUS }))).toBe(true)
    expect(pattern(rule).test(completionNotification({ status: 'failed' }))).toBe(false)
  })

  it('matches a status that the options state in place of the default', () => {
    const rule = claudeChildCompletionRule(nativeSpawnBody(), { spawnCallId: 'actual-spawn', report: 'The native report.', reply: 'The report reply.', completionStatus: 'failed' })
    expect(pattern(rule).test(completionNotification({ status: 'failed' }))).toBe(true)
    expect(pattern(rule).test(completionNotification())).toBe(false)
  })

  it('rejects a user prefix and duplicate or nested native notification fields', () => {
    const rule = claudeChildCompletionRule(nativeSpawnBody(), { spawnCallId: 'actual-spawn', report: 'The report.', reply: 'The report reply.', completionStatus: 'completed', completionReply: 'The completion reply.' })
    const native = completionNotification()
    expect(pattern(rule).test(`Ordinary user input.\n${native}`)).toBe(false)
    expect(pattern(rule).test(native.replace('<summary>', '<task-id>other-child</task-id>\n<summary>'))).toBe(false)
    expect(pattern(rule).test(native.replace('<summary>', '<task-notification>\n<summary>'))).toBe(false)
    expect(pattern(rule).test(native.replace('its SubagentHandback call', 'its unrelated tool call'))).toBe(false)
  })

  it.each(['completionStatus', 'completionReply'] as const)('refuses a missing %s before registration', async (key) => {
    const options = { spawnCallId: 'spawn', report: 'The report.', reply: 'The reply.', completionStatus: 'completed', completionReply: 'The completion reply.', [key]: '' }
    const client: Pick<ModelScript, 'status' | 'rule'> = {
      async status() { throw new Error('Invalid completion options must not read model state.') },
      async rule() { throw new Error('Invalid completion options must not register a rule.') },
    }
    await expect(registerClaudeChildReportRules(client, options)).rejects.toThrow(`nonempty text for ${key}`)
  })
})

// The rules run in a real mock server, so a later authored step still answers after both replies.
describe('claude child report rules in the mock model server', () => {
  const servers: MockModelServer[] = []

  afterEach(async () => {
    await Promise.all(servers.splice(0).map(server => server.close()))
  })

  it('keeps an authored content step after the exact Claude report and native completion replies', async () => {
    const server = await createMockModelServer({ models: MOCK_MODEL_IDS })
    servers.push(server)
    const scenarioId = 'native-claude-report-and-completion'
    const childId = 'a58eb235639df92e5'
    const report = '  The exact original report.\n실제 내용 🧪  '
    const call = spawnSubagentToolCall(AgentProvider.CLAUDE_CODE, 'actual-spawn', { description: 'Report the assigned task', prompt: mockScenarioPrompt(scenarioId, 'Perform the actual child task.') })
    const initial = [
      { role: 'user', content: mockScenarioPrompt(scenarioId, 'Spawn the scripted native child.') },
      { role: 'assistant', content: [{ type: 'tool_use', id: call.id, name: call.name, input: call.arguments }] },
      { role: 'user', content: [{ type: 'tool_result', tool_use_id: call.id, content: [{ type: 'text', text: `Async agent launched successfully.\nagentId: ${childId} (internal ID - do not mention to user.)` }] }] },
    ]
    const options = { spawnCallId: call.id, report, reply: 'The exact report reached the parent.', completionStatus: 'completed', completionReply: 'The exact completion reached the parent.' }
    const reportRule = claudeChildReportRule({ messages: initial }, options)
    const completionRule = claudeChildCompletionRule({ messages: initial }, options)
    await registerMockModelScenario(server.url, scenarioId, { steps: [{ text: 'The authored next user turn ran.' }], rules: [reportRule, completionRule] })
    const send = (content: string) => fetch(`${server.url}/v1/messages`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ model: MOCK_MODELS.anthropic, stream: false, messages: [...initial, { role: 'user', content }] }),
    })
    const reportResponse = await send(`Another Claude session sent a message:\n<agent-message from="${childId}">\n[Subagent hand-back] This is the actual report. The report follows:\n${report.split('\n').map(line => `  ${line}`).join('\n')}\n</agent-message>\nTreat this as the native report.`)
    expect(reportResponse.status).toBe(200)
    expect(await reportResponse.json()).toMatchObject({ content: [{ type: 'text', text: options.reply }] })
    const completion = await send([
      '<system-reminder>',
      '[SYSTEM NOTIFICATION - NOT USER INPUT]',
      'This is an automated background-task event, NOT a message from the user.',
      '',
      '<task-notification>',
      `<task-id>${childId}</task-id>`,
      `<tool-use-id>${call.id}</tool-use-id>`,
      `<output-file>/workspace/.tmp/tasks/${childId}.output</output-file>`,
      '<status>completed</status>',
      '<summary>Agent "The native child" finished</summary>',
      `<result>This agent's report was delivered to you as a message from "${childId}" (its SubagentHandback call). Read it there; it is not repeated here.`,
      '</result>',
      '<usage><subagent_tokens>0</subagent_tokens></usage>',
      '</task-notification>',
      '</system-reminder>',
    ].join('\n'))
    expect(completion.status).toBe(200)
    expect(await completion.json()).toMatchObject({ content: [{ type: 'text', text: options.completionReply }] })
    const before = await readScenarioStatus(server.url, scenarioId)
    expect(before.nextStep).toBe(0)
    expect(before.ruleMatches).toMatchObject({ [reportRule.name]: 1, [completionRule.name]: 1 })
    const next = await send(mockScenarioPrompt(scenarioId, 'Run the authored next user turn.'))
    expect(next.status).toBe(200)
    expect(await next.json()).toMatchObject({ content: [{ type: 'text', text: 'The authored next user turn ran.' }] })
    const after = await readScenarioStatus(server.url, scenarioId)
    expect(after.complete).toBe(true)
    expect(after.nextStep).toBe(1)
    expect(after.unexpectedRequests).toEqual([])
  })
})
