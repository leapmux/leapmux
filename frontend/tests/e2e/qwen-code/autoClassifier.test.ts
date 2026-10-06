import type { MockModelToolCall } from '../helpers/mockModelScript'
import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import { describe, expect, it, vi } from 'vitest'
import { makeMessage, rawContent } from '~/test-support/messageFactory'
import { ACP_UPDATE } from '../../../src/generated/contracts/acp-protocol'
import { matchesRequest, systemText } from '../helpers/mockModelScript'
import { expectQwenCanceledTool, qwenClassifierWithoutVerdict } from './autoClassifier'

vi.mock('@playwright/test', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@playwright/test')>()
  return { ...actual, expect }
})

const toolCall: MockModelToolCall = { id: 'smart-protected-write', name: 'write_file', arguments: { file_path: '/project/package.json', content: '{}' } }

/** A Worker snapshot whose one row stores `frame` in the span of the tool call. */
function snapshot(frame: unknown, spanId = toolCall.id): NativeMessageSnapshot {
  return {
    agentId: 'qwen-agent',
    agentSessionId: 'native-session',
    messages: [makeMessage({ id: 'row', seq: 1n, spanId, agentSessionId: 'native-session', content: rawContent(frame) })],
  }
}

function canceled(text = `Tool "${toolCall.name}" was canceled by the user.`, status = 'failed') {
  return { sessionUpdate: ACP_UPDATE.ToolCallUpdate, toolCallId: toolCall.id, status, content: [{ type: 'content', content: { type: 'text', text } }] }
}

describe('qwenClassifierWithoutVerdict', () => {
  it('answers the classifier request by its system prompt with text that states no verdict', () => {
    const rule = qwenClassifierWithoutVerdict('classifier')
    expect(rule.name).toBe('classifier')
    expect(rule.respond).toEqual({ text: 'The classifier states no verdict.' })
    expect(rule.respond.toolCalls).toBeUndefined()
  })

  it('matches the classifier request and no turn of the conversation', () => {
    const { when } = qwenClassifierWithoutVerdict('classifier')
    const request = (body: unknown) => ({ protocol: 'openai-chat-completions' as const, systemText: systemText('openai-chat-completions', body), userText: '', body })
    const classifier = { messages: [{ role: 'system', content: 'You are a security classifier for an AI coding agent operating in auto mode.' }, { role: 'user', content: '## Pending tool call to classify' }] }
    const turn = { messages: [{ role: 'system', content: 'You are Qwen Code, an interactive CLI agent.' }, { role: 'user', content: 'Run the scripted permission probe.' }] }
    expect(matchesRequest(when, request(classifier))).toBe(true)
    expect(matchesRequest(when, request(turn))).toBe(false)
  })

  it.each(['', ' '])('refuses the rule name %j', (name) => {
    expect(() => qwenClassifierWithoutVerdict(name)).toThrow('requires a name')
  })
})

describe('expectQwenCanceledTool', () => {
  it('accepts the failed update that states that the reader canceled the tool', () => {
    expect(() => expectQwenCanceledTool(snapshot(canceled()), toolCall)).not.toThrow()
  })

  it('refuses a failed update with another text', () => {
    expect(() => expectQwenCanceledTool(snapshot(canceled('The write failed.')), toolCall)).toThrow()
  })

  it('refuses an update that completed the tool', () => {
    expect(() => expectQwenCanceledTool(snapshot(canceled(undefined, 'completed')), toolCall)).toThrow('exactly one accepted record')
  })

  it('refuses an update in the span of another tool call', () => {
    expect(() => expectQwenCanceledTool(snapshot(canceled(), 'another-call'), toolCall)).toThrow('exactly one accepted record')
  })
})
