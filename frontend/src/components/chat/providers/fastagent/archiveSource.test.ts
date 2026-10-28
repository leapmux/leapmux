import { describe, expect, it } from 'vitest'
import { FASTAGENT_ARCHIVE } from '~/generated/contracts/fastagent-protocol'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerRow, providerToolCall } from '~/test-support/toolCallFixture'
import { providerFor } from '../registry'
import { input } from '../testUtils'
import { FAST_AGENT_ARCHIVE_REQUEST, fastAgentArchiveFixture } from './archiveFixture.test-support'
import { fastAgentArchiveSource, sameFastAgentArchiveCall } from './archiveSource'
import './plugin'

function archiveInput(raw: Record<string, unknown>, field: string, changes: Record<string, unknown> = {}) {
  const options = fastAgentArchiveFixture(field, changes)
  return { ...input(raw, undefined, AgentProvider.FAST_AGENT), agentSessionId: options.agentSessionId, supplementalContent: options.supplementalContent }
}

describe('fastAgentArchiveSource', () => {
  it('keeps native tool-result context out of human input rows', () => {
    const raw = { type: 'text', text: 'Native tool context, not human input.' }
    const options = fastAgentArchiveFixture(FASTAGENT_ARCHIVE.Content, { role: FASTAGENT_ARCHIVE.UserRole })
    const parsed = archiveInput(raw, FASTAGENT_ARCHIVE.Content, { role: FASTAGENT_ARCHIVE.UserRole })
    expect(providerFor(AgentProvider.FAST_AGENT)?.transcript.classify(parsed).kind).toBe('assistant_text')
    expect(providerRow(AgentProvider.FAST_AGENT, raw, options)).toEqual({ kind: 'assistant-text', text: raw.text })
  })

  it('preserves the native error channel on a tool-result user message', () => {
    const parsed = archiveInput({ type: 'text', text: 'native tool failure' }, FASTAGENT_ARCHIVE.Channels, { channel: FASTAGENT_ARCHIVE.ErrorChannel, role: FASTAGENT_ARCHIVE.UserRole })
    expect(fastAgentArchiveSource(parsed)).toMatchObject({ kind: 'error', text: 'native tool failure' })
    expect(providerFor(AgentProvider.FAST_AGENT)?.transcript.classify(parsed).kind).toBe('assistant_text')
  })

  it('keeps a native interruption outcome on the recovered tool result', () => {
    const result = { content: [{ type: 'text', text: '**The user interrupted this tool call**' }], isError: true }
    const options = fastAgentArchiveFixture(FASTAGENT_ARCHIVE.ToolResults)
    const call = providerToolCall(AgentProvider.FAST_AGENT, result, { ...options, completion: MessageCompletion.INTERRUPTED })
    expect(call).toMatchObject({ kind: 'execute', status: 'cancelled' })
  })

  it('keeps index zero, empty text, and an absent timestamp', () => {
    const parsed = archiveInput({ type: 'text', text: '' }, FASTAGENT_ARCHIVE.Content)
    expect(fastAgentArchiveSource(parsed)).toMatchObject({ kind: 'content', index: 0, messageIndex: 0, text: '' })
    expect(providerRow(AgentProvider.FAST_AGENT, parsed.parentObject!, fastAgentArchiveFixture(FASTAGENT_ARCHIVE.Content))).toEqual({ kind: 'assistant-text', text: '' })
  })

  it.each([
    [FASTAGENT_ARCHIVE.Reasoning, 'reasoning', 'assistant_thinking'],
    [FASTAGENT_ARCHIVE.ErrorChannel, 'error', 'assistant_text'],
    [FASTAGENT_ARCHIVE.TimingChannel, 'metadata', 'hidden'],
    [FASTAGENT_ARCHIVE.ToolTimingChannel, 'metadata', 'hidden'],
    [FASTAGENT_ARCHIVE.ToolMetadataChannel, 'metadata', 'hidden'],
    [FASTAGENT_ARCHIVE.UsageChannel, 'metadata', 'hidden'],
    [FASTAGENT_ARCHIVE.ShellProcessMetadataChannel, 'metadata', 'hidden'],
  ])('reads the native %s channel as %s', (channel, kind, category) => {
    const parsed = archiveInput({ type: 'text', text: 'native channel' }, FASTAGENT_ARCHIVE.Channels, { channel })
    expect(fastAgentArchiveSource(parsed)?.kind).toBe(kind)
    expect(providerFor(AgentProvider.FAST_AGENT)?.transcript.classify(parsed).kind).toBe(category)
  })

  it.each([
    { sessionId: 'foreign' },
    { captureId: '' },
    { actor: '' },
    { historyFile: '../history_agent.json' },
    { historyFile: 'history_a\\b.json' },
    { historyFile: 'history_a\0.json' },
    { messageIndex: -1 },
    { messageIndex: 0.5 },
    { messageIndex: Number.MAX_SAFE_INTEGER + 1 },
    { role: 'system' },
    { field: 'unknown' },
    { index: -1 },
    { index: 0.5 },
    { format: 'other' },
    { timestamp: 0 },
  ])('refuses invalid or foreign source selectors: %j', (changes) => {
    const parsed = archiveInput({ type: 'text', text: 'native' }, FASTAGENT_ARCHIVE.Content, changes)
    expect(fastAgentArchiveSource(parsed)).toBeUndefined()
    expect(providerFor(AgentProvider.FAST_AGENT)?.transcript.classify(parsed).kind).toBe('unknown')
  })

  it('matches a request and result only within one captured native call', () => {
    const opener = fastAgentArchiveSource(archiveInput(FAST_AGENT_ARCHIVE_REQUEST, FASTAGENT_ARCHIVE.ToolCalls))!
    const result = { content: [{ type: 'text', text: 'native result' }], isError: false }
    const closer = fastAgentArchiveSource(archiveInput(result, FASTAGENT_ARCHIVE.ToolResults))!
    expect(sameFastAgentArchiveCall(opener, closer)).toBe(true)
    for (const changes of [{ captureId: 'other' }, { actor: 'other' }, { historyFile: 'history_other.json' }, { callId: 'other' }, { spanId: 'other' }, { request: { ...FAST_AGENT_ARCHIVE_REQUEST, params: { ...FAST_AGENT_ARCHIVE_REQUEST.params, arguments: { command: 'other' } } } }]) {
      const foreign = fastAgentArchiveSource(archiveInput(result, FASTAGENT_ARCHIVE.ToolResults, changes))!
      expect(sameFastAgentArchiveCall(opener, foreign)).toBe(false)
    }
  })

  it('resolves a request card through its native result sibling', () => {
    const result = { content: [{ type: 'text', text: 'complete sibling output' }], isError: false }
    const parsedResult = archiveInput(result, FASTAGENT_ARCHIVE.ToolResults)
    const options = fastAgentArchiveFixture(FASTAGENT_ARCHIVE.ToolCalls)
    const call = providerToolCall(AgentProvider.FAST_AGENT, FAST_AGENT_ARCHIVE_REQUEST, { ...options, role: 'request', result: parsedResult })
    expect(call).toMatchObject({ kind: 'execute', status: 'completed', result: { commands: [{ output: 'complete sibling output' }] } })
    const foreign = archiveInput(result, FASTAGENT_ARCHIVE.ToolResults, { captureId: 'other' })
    const unpaired = providerToolCall(AgentProvider.FAST_AGENT, FAST_AGENT_ARCHIVE_REQUEST, { ...options, role: 'request', result: foreign })
    expect(unpaired?.status).toBe('in_progress')
  })

  it('preserves rich generic results and their exact original objects', () => {
    const request = { method: 'tools/call', params: { name: 'native_report', arguments: { count: 0 } } }
    const raw = { content: [{ type: 'image', data: 'aGVsbG8=', mimeType: 'image/png' }, { type: 'resource', resource: { uri: 'file:///report', text: 'native resource' } }], structuredContent: { count: 0 } }
    const original = JSON.stringify(raw)
    const options = fastAgentArchiveFixture(FASTAGENT_ARCHIVE.ToolResults, { toolName: 'native_report', request })
    const call = providerToolCall(AgentProvider.FAST_AGENT, raw, options)
    expect(call).toMatchObject({ kind: 'other', status: 'completed', request: { args: { count: 0 } }, result: { structuredJson: '{\n  "count": 0\n}' } })
    expect(call?.kind === 'other' && call.result && 'content' in call.result ? call.result.content : undefined).toHaveLength(2)
    expect(JSON.stringify(raw)).toBe(original)
  })

  it('keeps genuine ACP permissions, refusals, and dividers on the captured ACP plugin', () => {
    const parsed = input({ stopReason: 'end_turn' }, undefined, AgentProvider.FAST_AGENT)
    expect(providerFor(AgentProvider.FAST_AGENT)?.transcript.classify(parsed).kind).toBe('result_divider')
    const call = providerToolCall(AgentProvider.FAST_AGENT, { sessionUpdate: 'tool_call_update', toolCallId: 'real-live', title: 'execute', kind: 'execute', status: 'failed', rawInput: { command: 'refused' }, content: [{ type: 'content', content: { type: 'text', text: 'The user has declined permission to use this tool: local__execute' } }] })
    expect(call).toMatchObject({ id: 'real-live', status: 'declined' })
    const permission = providerFor(AgentProvider.FAST_AGENT)?.controls?.extractControl?.({ payload: {
      method: 'session/request_permission',
      params: {
        toolCall: { toolCallId: 'permission-call', title: 'Execute', kind: 'execute', rawInput: { command: 'printf permission' } },
        options: [{ optionId: 'allow_once', kind: 'allow_once', name: 'Allow once' }],
      },
    } })
    expect(permission).toMatchObject({ kind: 'permission', permission: { command: 'printf permission', options: [{ optionId: 'allow_once' }] } })
  })
})
