import type { AgentChatMessage } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { describe, expect, it } from 'vitest'
import { makeMessage, rawContent } from '~/test-support/messageFactory'
import { GEMINI_SUPPLEMENT } from '../../../src/generated/contracts/gemini-protocol'
import { ContentCompression } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { nativeMessageBody, nativeMessageSupplement } from '../helpers/nativeMessages'
import { computedNativeToolOutput } from '../helpers/nativeToolOutput'
import { readGeminiStoredToolRecord } from './toolRecord'

const encoder = new TextEncoder()
const callId = 'run_shell_command__gemini-native-record'
const frame = { sessionUpdate: 'tool_call_update', toolCallId: callId, status: 'completed', kind: 'execute', content: [] }

function toolRecord(output = 'Native output'): Record<string, unknown> {
  return {
    id: callId,
    name: 'run_shell_command',
    status: 'success',
    args: { command: 'printf native', count: 0, enabled: false, text: '' },
    result: [{ functionResponse: { id: 'gemini-native-record', name: 'run_shell_command', response: { output } } }],
    resultDisplay: output,
  }
}

function provider(record: unknown = toolRecord()): Record<string, unknown> {
  return { sessionUpdate: frame.sessionUpdate, toolCallId: frame.toolCallId, status: frame.status, rawOutput: { [GEMINI_SUPPLEMENT.StoredToolRecord]: record } }
}

function message(envelope: unknown, original: unknown = frame): AgentChatMessage {
  return makeMessage({
    id: 'actual-native-tool-row',
    spanId: callId,
    spanType: 'execute',
    agentSessionId: '17e04c30-fc15-44c1-801f-93e0dcba0c32',
    content: rawContent(original),
    supplementalContent: rawContent(envelope),
  })
}

describe('readGeminiStoredToolRecord', () => {
  it('reads complete native output from the actual Worker provider envelope', () => {
    const output = computedNativeToolOutput({ prefix: 'GEMININATIVE', lineCount: 8000, padding: 12 })
    const record = toolRecord(output.text)
    const original = { ...frame, content: [{ type: 'content', content: { type: 'text', text: 'NATIVE_EXCERPT_ONLY' } }] }
    const envelope = { provider: provider(record), metadata: { elapsed_ms: 0 } }
    const row = message(envelope, original)
    const before = { content: row.content.slice(), supplementalContent: row.supplementalContent.slice() }
    const read = readGeminiStoredToolRecord(row)
    expect(read).not.toBeNull()
    expect(read).toEqual(record)
    expect(read?.resultDisplay).toBe(output.text)
    expect(read?.resultDisplay).toContain(output.omittedMarker)
    expect(read?.resultDisplay).toContain(output.lastMarker)
    expect(read?.args).toEqual({ command: 'printf native', count: 0, enabled: false, text: '' })
    expect(nativeMessageBody(row)).toEqual(original)
    expect(nativeMessageSupplement(row)).toEqual(envelope)
    expect(row.content).toEqual(before.content)
    expect(row.supplementalContent).toEqual(before.supplementalContent)
  })

  it('ignores outer metadata and reads an exact failed native record', () => {
    const record = { ...toolRecord('Native failure'), status: 'error' }
    const original = { ...frame, status: 'failed' }
    const extra = { ...provider(record), status: 'failed' }
    expect(readGeminiStoredToolRecord(message({ provider: extra, metadata: { rawOutput: { [GEMINI_SUPPLEMENT.StoredToolRecord]: toolRecord('METADATA_ONLY_OUTPUT') } } }, original))).toEqual(record)
  })

  it('preserves empty native output and future native fields', () => {
    const record = { ...toolRecord(''), future: { zero: 0, negative: -1, enabled: false, text: '' } }
    expect(readGeminiStoredToolRecord(message({ provider: provider(record) }))).toEqual(record)
  })

  it('rejects top-level and metadata records outside the provider section', () => {
    const extra = provider()
    expect(readGeminiStoredToolRecord(message(extra))).toBeNull()
    expect(readGeminiStoredToolRecord(message({ metadata: extra }))).toBeNull()
    expect(readGeminiStoredToolRecord(message({ ...extra, provider: null }))).toBeNull()
  })

  it.each([null, false, 0, '', [], {}, { provider: null }, { provider: false }, { provider: 0 }, { provider: '' }, { provider: [] }].map(envelope => [envelope]))('rejects an absent or malformed provider envelope: %j', (envelope) => {
    expect(readGeminiStoredToolRecord(message(envelope))).toBeNull()
  })

  it.each([
    { toolCallId: 'run_shell_command__another-call' },
    { status: 'pending' },
    { sessionUpdate: 'tool_call' },
  ])('rejects a supplement with another frame identity: %j', (changes) => {
    expect(readGeminiStoredToolRecord(message({ provider: { ...provider(), ...changes } }))).toBeNull()
  })

  it.each(['toolCallId', 'status', 'sessionUpdate'])('rejects an identity property absent on one side: %s', (key) => {
    const original: Record<string, unknown> = { ...frame }
    delete original[key]
    expect(readGeminiStoredToolRecord(message({ provider: provider() }, original))).toBeNull()
    const extra = provider()
    delete extra[key]
    expect(readGeminiStoredToolRecord(message({ provider: extra }))).toBeNull()
  })

  it.each([null, false, 0, '', [], {}].map(value => [value]))('rejects a missing or malformed native record: %j', (value) => {
    expect(readGeminiStoredToolRecord(message({ provider: provider(value) }))).toBeNull()
  })

  it.each([
    { id: 'run_shell_command__another-call' },
    { id: '' },
    { name: 'read_file' },
    { name: '' },
    { status: 'pending' },
    { status: '' },
  ])('rejects an unrelated or invalid native record: %j', (changes) => {
    expect(readGeminiStoredToolRecord(message({ provider: provider({ ...toolRecord(), ...changes }) }))).toBeNull()
  })

  it.each([null, false, 0, '', []].map(original => [original]))('rejects a malformed original frame: %j', (original) => {
    expect(readGeminiStoredToolRecord(message({ provider: provider() }, original))).toBeNull()
  })

  it('rejects invalid original and supplemental bytes without changing them', () => {
    for (const bytes of [encoder.encode('{broken'), new Uint8Array([0xFF, 0xFE])]) {
      const original = makeMessage({ ...message({ provider: provider() }), content: bytes })
      expect(readGeminiStoredToolRecord(original)).toBeNull()
      expect(original.content).toEqual(bytes)
      const supplemental = makeMessage({ ...message({ provider: provider() }), supplementalContent: bytes })
      expect(readGeminiStoredToolRecord(supplemental)).toBeNull()
      expect(supplemental.supplementalContent).toEqual(bytes)
    }
  })

  it('rejects unsupported compression without changing retained bytes', () => {
    const original = makeMessage({ ...message({ provider: provider() }), contentCompression: ContentCompression.UNSPECIFIED })
    expect(readGeminiStoredToolRecord(original)).toBeNull()
    const supplemental = makeMessage({ ...message({ provider: provider() }), supplementalContentCompression: ContentCompression.UNSPECIFIED })
    expect(readGeminiStoredToolRecord(supplemental)).toBeNull()
  })
})
