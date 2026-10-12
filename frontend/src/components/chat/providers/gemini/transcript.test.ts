import type { ParsedMessageContent } from '~/lib/messageParser'
import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { AgentChatMessageSchema, AgentProvider, MessageCompletion, MessageSource } from '~/generated/proto/leapmux/v1/agent_pb'
import { toClassificationInput } from '../../messageClassifier'
import { providerFor, resolveMessageForRendering } from '../registry'
import './plugin'

const plugin = providerFor(AgentProvider.GEMINI_CLI)!

function native(frame: Record<string, unknown>, supplementalContent?: unknown, completion = MessageCompletion.COMPLETE) {
  const parsed: ParsedMessageContent = { rawText: JSON.stringify(frame), parentObject: frame, topLevel: frame, wrapper: null, supplementalContent }
  const resolved = resolveMessageForRendering(parsed, AgentProvider.GEMINI_CLI)
  const category = plugin.transcript.classify(toClassificationInput(resolved, create(AgentChatMessageSchema, { agentProvider: AgentProvider.GEMINI_CLI, source: MessageSource.AGENT, completion })))
  return { parsed, resolved, category, row: plugin.transcript.extractRow({ resolved, category, completion, span: { request: undefined, result: undefined, role: 'other', visibleRows: { request: false, result: false } } }) }
}

describe('composeGeminiTranscript', () => {
  it('reads the original native child prompt and model text without ACP frames', () => {
    const prompt = native({ id: 'native-user', type: 'user', content: [{ text: 'Read the child file.' }] }, { geminiMessagePart: 'content' })
    expect(prompt.category).toEqual({ kind: 'user_text' })
    expect(prompt.row).toEqual({ kind: 'user', text: 'Read the child file.', attachments: [] })
    expect(plugin.transcript.spanRole(prompt.resolved)).toBe('other')
    expect(plugin.transcript.relatedMessages?.(prompt.resolved)).toEqual([])
    const answer = native({ id: 'native-model', type: 'gemini', content: [{ text: 'Private reasoning', thought: true }, { text: 'Complete child answer.' }] }, { geminiMessagePart: 'content' })
    expect(answer.category).toEqual({ kind: 'assistant_text' })
    expect(answer.row).toEqual({ kind: 'assistant-text', text: 'Complete child answer.' })
    expect(answer.parsed.parentObject).not.toHaveProperty('sessionUpdate')
  })

  it('selects a complete native thought without discarding the content of the same message', () => {
    const frame = { id: 'native-model', type: 'gemini', content: 'Native answer.', thoughts: [{ subject: 'Inspect', description: 'Read the actual source.' }, { subject: '', description: 'Preserve every byte.' }] }
    expect(native(frame, { geminiMessagePart: 'thought', geminiMessagePartIndex: 0 }).row).toEqual({ kind: 'assistant-thinking', text: '**Inspect**\nRead the actual source.' })
    expect(native(frame, { geminiMessagePart: 'thought', geminiMessagePartIndex: 1 }).row).toEqual({ kind: 'assistant-thinking', text: 'Preserve every byte.' })
    expect(native(frame, { geminiMessagePart: 'content' }).row).toEqual({ kind: 'assistant-text', text: 'Native answer.' })
    for (const index of [-1, 0.5, 2, undefined, Number.MAX_SAFE_INTEGER])
      expect(native(frame, { geminiMessagePart: 'thought', geminiMessagePartIndex: index }).row).toBeNull()
  })

  it('hides function-response-only records and empty native content', () => {
    for (const content of ['', [], [{ functionResponse: { response: { output: 'another tool result' } } }]]) {
      const result = native({ id: 'native-user', type: 'user', content })
      expect(result.category).toEqual({ kind: 'hidden' })
      expect(result.row).toEqual({ kind: 'hidden' })
    }
  })

  it('reads an exact native child tool and preserves its failure and computed output', () => {
    const frame = { id: 'run_shell_command__native-tool', name: 'run_shell_command', status: 'success', args: { command: 'printf native; exit 7' }, resultDisplay: 'native', result: [{ functionResponse: { response: { output: 'Output: native\nExit Code: 7\nProcess Group PGID: 123' } } }] }
    const result = native(frame)
    expect(result.category).toEqual({ kind: 'tool_use' })
    expect(plugin.transcript.spanRole(result.resolved)).toBe('result')
    expect(result.row).toMatchObject({ kind: 'tool', role: 'result', call: { id: frame.id, kind: 'execute', status: 'failed', request: { command: 'printf native; exit 7' }, result: { commands: [{ output: 'native', exitCode: 7 }] } } })
    expect(result.parsed.parentObject).toBe(frame)
  })

  it('delegates ordinary ACP rows to the captured base without recursive registry lookup', () => {
    const result = native({ sessionUpdate: 'tool_call', toolCallId: 'read_file__native-tool', status: 'in_progress', kind: 'read', locations: [{ path: '/native/file.txt' }] }, undefined, MessageCompletion.UNSPECIFIED)
    expect(result.category).toEqual({ kind: 'tool_use' })
    expect(result.row).toMatchObject({ kind: 'tool', role: 'request', call: { kind: 'read', request: { path: '/native/file.txt' } } })
  })
})
