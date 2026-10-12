import type { MockModelRequestRecord, MockModelRule } from '../helpers/mockModelScript'
import { describe, expect, it } from 'vitest'
import { matchesRequest } from '../helpers/mockModelScript'
import { lettaChildLaunch, lettaChildNoticeRule } from './childNoticeRule'

const options = { name: 'the actual Letta task completed', spawnCallId: 'native-letta-spawn', description: 'Read "A&B<file>"', report: 'REPORT & <native>\nThe report ends here.', reply: 'The actual Letta report arrived.' }
const launch = { spawnCallId: options.spawnCallId, description: options.description, taskId: 'task_42', outputFile: '/private/A&B/task_42.output', agentId: 'agent-native', conversationId: 'conversation-native' }
const childId = 'subagent-1234567890-1'
const xml = (text: string) => text.replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;')

function receipt(text = `Task running in background with task ID: ${launch.taskId}\nAgent ID: ${launch.agentId}\nConversation ID: ${launch.conversationId}\nOutput file: ${launch.outputFile}\n`, callName = 'Agent'): MockModelRequestRecord {
  return {
    protocol: 'openai-chat-completions',
    path: '/v1/chat/completions',
    body: { messages: [{ role: 'assistant', tool_calls: [{ id: options.spawnCallId, function: { name: callName, arguments: JSON.stringify({ description: options.description, prompt: 'Perform the actual child task.', subagent_type: 'general-purpose' }) } }] }, { role: 'tool', tool_call_id: options.spawnCallId, content: text }] },
  }
}

function notice() {
  const result = `subagent_type=general-purpose subagent_id=${childId} subagent_status=success agent_id=${launch.agentId} conversation_id=${launch.conversationId}\n\n${options.report}`
  return `<task-notification>\n<task-id>${launch.taskId}</task-id>\n<status>completed</status>\n<summary>${xml(`Agent "${options.description}" completed`)}</summary>\n<result>${xml(result)}</result>\n<usage>total_tokens: 0\ntool_uses: 1\nduration_ms: 0</usage>\n</task-notification>\nFull transcript available at: ${launch.outputFile}`
}

function accepts(rule: MockModelRule, text: string, lastRole = 'user') {
  return matchesRequest(rule.when, { protocol: 'openai-chat-completions', systemText: '', userText: text, body: { messages: [{ role: 'user', content: text }, ...(lastRole === 'user' ? [] : [{ role: lastRole, content: 'A later native message.' }])] } })
}

describe('lettaChildLaunch', () => {
  it('reads the actual native task, output file, and optional backend identities', () => {
    const request = receipt()
    expect(lettaChildLaunch(request, options.spawnCallId)).toEqual(launch)
    expect(request.body).toEqual(receipt().body)
  })

  it('retains a receipt without optional backend identities', () => {
    expect(lettaChildLaunch(receipt(`Task running in background with task ID: ${launch.taskId}\nOutput file: ${launch.outputFile}\n`), options.spawnCallId)).toEqual({ spawnCallId: options.spawnCallId, description: options.description, taskId: launch.taskId, outputFile: launch.outputFile })
  })

  it('ignores request text and arguments without an actual matched result', () => {
    expect(lettaChildLaunch({ protocol: 'openai-chat-completions', path: '/', body: { messages: [{ role: 'user', content: 'Task running in background with task ID: task_999' }] } }, options.spawnCallId)).toBeNull()
    expect(lettaChildLaunch(receipt(), 'foreign-call')).toBeNull()
  })

  it.each([
    'Task launch failed.',
    `Task running in background with task ID: task_bad\nOutput file: ${launch.outputFile}\n`,
    `Task running in background with task ID: ${launch.taskId}\nTask running in background with task ID: task_99\nOutput file: ${launch.outputFile}\n`,
    `Task running in background with task ID: ${launch.taskId}\n`,
    `Task running in background with task ID: ${launch.taskId}\nOutput file: /first\nOutput file: /second\n`,
    `Task running in background with task ID: ${launch.taskId}\nAgent ID: agent-one\nAgent ID: agent-two\nOutput file: ${launch.outputFile}\n`,
  ])('rejects an invalid or ambiguous native task receipt: %j', (text) => {
    expect(() => lettaChildLaunch(receipt(text), options.spawnCallId)).toThrow('unique native task identity')
  })

  it('rejects a result whose call ID belongs to another native tool', () => {
    expect(() => lettaChildLaunch(receipt(undefined, 'Bash'), options.spawnCallId)).toThrow('unique actual Agent call')
  })
})

describe('lettaChildNoticeRule', () => {
  it('matches the actual task and child IDs with the native result header', () => {
    const rule = lettaChildNoticeRule(launch, childId, options)
    expect(accepts(rule, notice())).toBe(true)
    expect(rule.once).toBeUndefined()
    expect(accepts(rule, notice())).toBe(true)
  })

  it('keeps the original one-shot rule and accepts absent optional usage', () => {
    const rule = lettaChildNoticeRule(launch, childId, { ...options, once: true })
    expect(rule.once).toBe(true)
    expect(accepts(rule, notice().replace('\n<usage>total_tokens: 0\ntool_uses: 1\nduration_ms: 0</usage>', ''))).toBe(true)
  })

  it('accepts the native runtime session field without guessing its identity', () => {
    expect(accepts(lettaChildNoticeRule(launch, childId, options), notice().replace('conversation_id=conversation-native', 'conversation_id=conversation-native runtime_session_id=runtime-native'))).toBe(true)
  })

  it.each([
    ['the task ID', launch.taskId, 'task_99'],
    ['the native subagent ID', childId, 'subagent-1234567890-2'],
    ['the backend agent ID', launch.agentId, 'agent-foreign'],
    ['the backend conversation ID', launch.conversationId, 'conversation-foreign'],
    ['the summary', xml(options.description), 'A foreign task'],
    ['the actual report', xml(options.report), 'A foreign report'],
    ['the completion status', '<status>completed</status>', '<status>failed</status>'],
    ['the native result status', 'subagent_status=success', 'subagent_status=error'],
    ['the output footer', `Full transcript available at: ${launch.outputFile}`, 'Full transcript available at: /foreign/output'],
  ])('rejects a notice with the wrong %s', (_field, from, to) => {
    expect(accepts(lettaChildNoticeRule(launch, childId, options), notice().replaceAll(from, to))).toBe(false)
  })

  it.each(['prefix', 'quoted', 'nested', 'duplicated', 'monitor'])('rejects an unrelated or malformed %s envelope', (kind) => {
    const text = kind === 'prefix'
      ? `An unrelated user message.\n${notice()}`
      : kind === 'quoted'
        ? `The literal "${notice()}"`
        : kind === 'nested'
          ? notice().replace('<result>', '<result><task-notification>')
          : kind === 'duplicated'
            ? `${notice()}\n${notice()}`
            : notice().replace('<summary>Agent ', '<summary>Monitor event: ')
    expect(accepts(lettaChildNoticeRule(launch, childId, options), text)).toBe(false)
  })

  it('rejects stale history when the last native message is not the notice', () => {
    expect(accepts(lettaChildNoticeRule(launch, childId, options), notice(), 'assistant')).toBe(false)
    expect(accepts(lettaChildNoticeRule(launch, childId, options), notice(), 'tool')).toBe(false)
  })

  it('rejects an absent output path, a guessed task, or a guessed call', () => {
    expect(() => lettaChildNoticeRule({ ...launch, outputFile: '' }, childId, options)).toThrow('outputFile')
    expect(() => lettaChildNoticeRule({ ...launch, taskId: 'task_bad' }, childId, options)).toThrow('actual native task and subagent IDs')
    expect(() => lettaChildNoticeRule(launch, '', options)).toThrow('actual native task and subagent IDs')
    expect(() => lettaChildNoticeRule(launch, childId, { ...options, spawnCallId: 'foreign' })).toThrow('actual Agent call')
  })
})
