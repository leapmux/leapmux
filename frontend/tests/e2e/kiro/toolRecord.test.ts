import type { AgentChatMessage } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { describe, expect, it } from 'vitest'
import { makeMessage, rawContent } from '~/test-support/messageFactory'
import { expectSameBytes } from '~/test-support/sameBytes'
import { ContentCompression } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { pickObject } from '../../../src/lib/jsonPick'
import { nativeMessageBody, nativeMessageSupplement } from '../helpers/nativeMessages'
import { computedNativeToolOutput } from '../helpers/nativeToolOutput'
import { readKiroToolSupplement } from './toolRecord'

const encoder = new TextEncoder()
const callId = 'run_command_native-record-bytes'
const sessionId = 'sess_7dad5322-3c58-4fa4-87f0-8c976c20a628'
const path = `/private/native/.kiro/sessions/87a764e53071f92d/${sessionId}/tool-outputs/execute_bash-2ce62cab.txt`
const frame = {
  sessionUpdate: 'tool_call_update',
  toolCallId: callId,
  status: 'completed',
  kind: 'execute',
  rawInput: { command: 'node native-output.js', count: 0, enabled: false, text: '' },
  rawOutput: { output: 'NATIVE_EXCERPT_ONLY', exitCode: 0, message: `Full output: ${path}` },
}

function provider(text = 'Native output'): Record<string, unknown> {
  return {
    sessionUpdate: frame.sessionUpdate,
    toolCallId: frame.toolCallId,
    status: frame.status,
    rawOutput: { output: text },
  }
}

function message(envelope: unknown, original: unknown = frame): AgentChatMessage {
  return makeMessage({
    id: 'actual-native-tool-row',
    spanId: callId,
    spanType: 'execute',
    agentSessionId: sessionId,
    content: rawContent(original),
    supplementalContent: rawContent(envelope),
  })
}

describe('readKiroToolSupplement', () => {
  it('reads native output bytes from the actual Worker provider envelope', () => {
    const output = computedNativeToolOutput({ lineCount: 3000, padding: 30 })
    const full = `Output:\n${output.text}\n\nExit Code: 0`
    const receipt = provider(full)
    const original = { ...frame, _meta: { kiro: { outputTransformation: { kind: 'offloaded', absFilePath: path, totalChars: full.length } } } }
    const envelope = { provider: receipt, metadata: { elapsed_ms: 0 } }
    const row = message(envelope, original)
    const before = { content: row.content.slice(), supplementalContent: row.supplementalContent.slice() }
    const read = readKiroToolSupplement(row)
    expect(read).toEqual(receipt)
    const retained = pickObject(read, 'rawOutput')
    expect(retained).toEqual({ output: full })
    expect(retained?.output).toContain(output.firstMarker)
    expect(retained?.output).toContain(output.omittedMarker)
    expect(retained?.output).toContain(output.lastMarker)
    expect(nativeMessageBody(row)).toEqual(original)
    expect(nativeMessageSupplement(row)).toEqual(envelope)
    // The supplement holds the complete output, so `toEqual` would compare its bytes one at a time for most of a second.
    expectSameBytes(row.content, before.content, 'the original bytes')
    expectSameBytes(row.supplementalContent, before.supplementalContent, 'the supplemental bytes')
  })

  it('ignores outer metadata and keeps an exact failed receipt', () => {
    const receipt = { ...provider('Native failure'), status: 'failed' }
    const original = { ...frame, status: 'failed' }
    expect(readKiroToolSupplement(message({ provider: receipt, metadata: provider('METADATA_ONLY_OUTPUT') }, original))).toEqual(receipt)
  })

  it('keeps empty output and future native fields', () => {
    const receipt = { ...provider(''), future: { zero: 0, negative: -1, enabled: false, text: '' } }
    expect(readKiroToolSupplement(message({ provider: receipt }))).toEqual(receipt)
  })

  it('rejects top-level and metadata receipts outside the provider section', () => {
    const receipt = provider()
    expect(readKiroToolSupplement(message(receipt))).toBeNull()
    expect(readKiroToolSupplement(message({ metadata: receipt }))).toBeNull()
    expect(readKiroToolSupplement(message({ ...receipt, provider: null }))).toBeNull()
  })

  it.each([null, false, 0, '', [], {}, { provider: null }, { provider: false }, { provider: 0 }, { provider: '' }, { provider: [] }].map(envelope => [envelope]))('rejects an absent or malformed provider envelope: %j', (envelope) => {
    expect(readKiroToolSupplement(message(envelope))).toBeNull()
  })

  it.each([
    { toolCallId: 'run_command_another-call' },
    { status: 'pending' },
    { sessionUpdate: 'tool_call' },
  ])('rejects a receipt with another frame identity: %j', (changes) => {
    expect(readKiroToolSupplement(message({ provider: { ...provider(), ...changes } }))).toBeNull()
  })

  it.each(['toolCallId', 'status', 'sessionUpdate'])('rejects an identity property absent on one side: %s', (key) => {
    const original: Record<string, unknown> = { ...frame }
    delete original[key]
    expect(readKiroToolSupplement(message({ provider: provider() }, original))).toBeNull()
    const receipt = provider()
    delete receipt[key]
    expect(readKiroToolSupplement(message({ provider: receipt }))).toBeNull()
  })

  it.each([null, false, 0, '', []].map(original => [original]))('rejects a malformed original frame: %j', (original) => {
    expect(readKiroToolSupplement(message({ provider: provider() }, original))).toBeNull()
  })

  it('rejects invalid original and supplemental bytes without changing them', () => {
    for (const bytes of [encoder.encode('{broken'), new Uint8Array([0xFF, 0xFE])]) {
      const original = makeMessage({ ...message({ provider: provider() }), content: bytes })
      expect(readKiroToolSupplement(original)).toBeNull()
      expect(original.content).toEqual(bytes)
      const supplemental = makeMessage({ ...message({ provider: provider() }), supplementalContent: bytes })
      expect(readKiroToolSupplement(supplemental)).toBeNull()
      expect(supplemental.supplementalContent).toEqual(bytes)
    }
  })

  it('rejects unsupported compression without changing retained bytes', () => {
    const original = makeMessage({ ...message({ provider: provider() }), contentCompression: ContentCompression.UNSPECIFIED })
    expect(readKiroToolSupplement(original)).toBeNull()
    const supplemental = makeMessage({ ...message({ provider: provider() }), supplementalContentCompression: ContentCompression.UNSPECIFIED })
    expect(readKiroToolSupplement(supplemental)).toBeNull()
  })
})
