import type { ModelRequestContext } from './mockModelRequest'
import type { MockModelProtocol, MockModelStep } from './mockModelScript'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { claudeLifecycleAnswer, claudeRateLimitHeaders, prepareClaudeMessageStep } from './claudeSurface'
import { claudeSubagentHandbackToolCall, claudeSubagentHandbackToolDefinition, readToolCall } from './providerToolCalls'

const catalog = { tools: [claudeSubagentHandbackToolDefinition()] }

function deliveredBody(report: string) {
  const call = claudeSubagentHandbackToolCall('actual-report-id', report)
  return { ...catalog, messages: [
    { role: 'assistant', content: [{ type: 'tool_use', id: call.id, name: call.name, input: call.arguments }] },
    { role: 'user', content: [{ type: 'tool_result', tool_use_id: call.id, content: JSON.stringify({ success: true, message: 'Report delivered to your caller.' }) }] },
  ] }
}

function context(body: unknown, protocol: MockModelProtocol = 'anthropic-messages'): ModelRequestContext {
  return { body, protocol, path: '/v1/messages', userText: '', systemText: '' }
}

describe('claudeLifecycleAnswer', () => {
  it('retains the exact child report and fixed accounting rule without changing the request', () => {
    const body = deliveredBody('  Original report.\n실제 내용 🧪\t ')
    const original = JSON.stringify(body)
    expect(claudeLifecycleAnswer(context(body))).toEqual({ rule: 'claude-child-handback-complete', step: { text: '  Original report.\n실제 내용 🧪\t ' } })
    expect(JSON.stringify(body)).toBe(original)
  })

  it.each(['openai-responses', 'openai-chat-completions', 'aws-event-stream'] satisfies MockModelProtocol[])('does not interpret native child messages in %s', (protocol) => {
    expect(claudeLifecycleAnswer(context(deliveredBody('Actual report.'), protocol))).toBeUndefined()
  })

  it.each([null, {}, { ...catalog, messages: [] }, { tools: [], messages: deliveredBody('Actual report.').messages }])('leaves an absent or root child catalog unanswered: %j', (body) => {
    expect(claudeLifecycleAnswer(context(body))).toBeUndefined()
  })

  it('leaves a later root content turn to the explicit script', () => {
    const body = deliveredBody('Actual report.')
    expect(claudeLifecycleAnswer(context({ ...body, messages: [...body.messages, { role: 'user', content: 'Next actual parent task.' }] }))).toBeUndefined()
  })
})

describe('prepareClaudeMessageStep', () => {
  it('adds the native report tool without changing report whitespace or the original step', () => {
    const step = { text: '  Actual report.\n실제 내용 ', reasoning: 'Actual thinking.' }
    const before = JSON.stringify(step)
    const result = prepareClaudeMessageStep('anthropic-messages', catalog, step, 'native-response')
    expect(result).not.toBe(step)
    expect(result).toEqual({ ...step, toolCalls: [claudeSubagentHandbackToolCall('native-response-handback', step.text)] })
    expect(JSON.stringify(step)).toBe(before)
  })

  const preserved: MockModelStep[] = [
    {},
    { text: '' },
    { text: ' \n\t' },
    { reasoning: 'Only thinking.' },
    { error: { status: 500, message: 'Actual failure.' } },
    { text: 'Read instead.', toolCalls: [readToolCall(AgentProvider.CLAUDE_CODE, 'preserved-read', '/private/file.txt')] },
  ]
  it.each(preserved)('retains an empty, error, or native-tool step: %j', (step) => {
    expect(prepareClaudeMessageStep('anthropic-messages', catalog, step, 'response')).toBe(step)
  })

  it('retains a completed handback and never sends the report twice', () => {
    const step = { text: 'Actual report.' }
    expect(prepareClaudeMessageStep('anthropic-messages', deliveredBody(step.text), step, 'response')).toBe(step)
  })

  it('retains root steps and steps from every other model protocol', () => {
    const step = { text: 'Actual report.' }
    expect(prepareClaudeMessageStep('anthropic-messages', { tools: [] }, step, 'response')).toBe(step)
    for (const protocol of ['openai-responses', 'openai-chat-completions', 'aws-event-stream'] satisfies MockModelProtocol[])
      expect(prepareClaudeMessageStep(protocol, catalog, step, 'response')).toBe(step)
  })
})

describe('claudeRateLimitHeaders', () => {
  it.each([
    { type: 'five_hour', window: '5h' },
    { type: 'seven_day', window: '7d' },
    { type: 'seven_day_sonnet', window: '7d' },
    { type: 'weekly', window: '5h' },
    { type: '', window: '5h' },
  ])('keeps the native window and zero optional values for $type', ({ type, window }) => {
    expect(claudeRateLimitHeaders({ type, status: 'allowed_warning', utilization: 0, resetsAt: 0 })).toEqual({
      'anthropic-ratelimit-unified-status': 'allowed_warning',
      'anthropic-ratelimit-unified-representative-claim': type,
      [`anthropic-ratelimit-unified-${window}-utilization`]: '0',
      [`anthropic-ratelimit-unified-${window}-reset`]: '0',
      'anthropic-ratelimit-unified-reset': '0',
      [`anthropic-ratelimit-unified-${window}-surpassed-threshold`]: '0',
    })
  })

  it.each(['allowed', 'rejected', 'exceeded'])('omits absent values and the warning threshold for %s', (status) => {
    expect(claudeRateLimitHeaders({ type: 'five_hour', status })).toEqual({
      'anthropic-ratelimit-unified-status': status,
      'anthropic-ratelimit-unified-representative-claim': 'five_hour',
    })
  })

  it('retains a large reset and does not mutate the source quota', () => {
    const limits = Object.freeze({ type: 'seven_day', status: 'allowed_warning', utilization: 1, resetsAt: Number.MAX_SAFE_INTEGER })
    expect(claudeRateLimitHeaders(limits)).toHaveProperty('anthropic-ratelimit-unified-7d-reset', String(Number.MAX_SAFE_INTEGER))
    expect(limits.resetsAt).toBe(Number.MAX_SAFE_INTEGER)
  })
})
