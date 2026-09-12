import type { ParsedMessageContent } from '~/lib/messageParser'
import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { AgentChatMessageSchema, ContentCompression, MessageCompletion, MessageSource } from '~/generated/proto/leapmux/v1/agent_pb'
import { prettifyJson } from '~/lib/jsonFormat'
import { parseMessageContent } from '~/lib/messageParser'
import { buildRawJsonEnvelope } from './chatRawJson'

interface MsgOver {
  spanId?: string
  spanType?: string
  parentSpanId?: string
  spanColor?: number
  spanLines?: string
  depth?: number
}

function msg(over: MsgOver = {}) {
  return create(AgentChatMessageSchema, { id: 'm1', source: MessageSource.USER, seq: 7n, createdAt: 'T', ...over })
}

function parsed(over: Partial<ParsedMessageContent> = {}): ParsedMessageContent {
  return { rawText: '', topLevel: null, parentObject: undefined, wrapper: null, ...over }
}

describe('buildRawJsonEnvelope', () => {
  it('keeps undecodable bytes distinct from an empty original payload', () => {
    const message = create(AgentChatMessageSchema, {
      content: new Uint8Array([1, 2, 3]),
      contentCompression: ContentCompression.ZSTD,
    })
    const output = JSON.parse(buildRawJsonEnvelope(message, parseMessageContent(message), 'agent'))
    expect(output.content_decode_failed).toBe(true)
    expect(output.content).toEqual({ compression: ContentCompression.ZSTD, base64: 'AQID' })
    const empty = create(AgentChatMessageSchema, { contentCompression: ContentCompression.NONE })
    const emptyOutput = JSON.parse(buildRawJsonEnvelope(empty, parseMessageContent(empty), 'agent'))
    expect(emptyOutput.content).toBe('')
    expect(emptyOutput.content_decode_failed).toBeUndefined()
  })

  it('preserves numeric literals and repeated keys in both stored sources', () => {
    const raw = '{"wide":9007199254740993,"huge":1e400,"zero":-0,"key":1,"key":2}'
    const message = create(AgentChatMessageSchema, {
      content: new TextEncoder().encode(raw),
      contentCompression: ContentCompression.NONE,
      supplementalContent: new TextEncoder().encode(raw),
      supplementalContentCompression: ContentCompression.NONE,
    })
    const output = buildRawJsonEnvelope(message, parseMessageContent(message), 'agent')
    expect(output.match(/9007199254740993/g)).toHaveLength(2)
    expect(output.match(/1e400/g)).toHaveLength(2)
    expect(output.match(/"zero":-0/g)).toHaveLength(2)
    expect(output.match(/"key":1,"key":2/g)).toHaveLength(2)
    expect(prettifyJson(output)).toContain('9007199254740993')
    expect(prettifyJson(output)).toContain('1e400')
  })

  it('keeps supplemental data and completion when the original payload is invalid', () => {
    const message = create(AgentChatMessageSchema, {
      content: new TextEncoder().encode('invalid provider text'),
      contentCompression: ContentCompression.NONE,
      supplementalContent: new TextEncoder().encode('{"recovered":true}'),
      supplementalContentCompression: ContentCompression.NONE,
      completion: MessageCompletion.INTERRUPTED,
    })
    const output = JSON.parse(buildRawJsonEnvelope(message, parseMessageContent(message), 'agent'))
    expect(output.content).toBe('invalid provider text')
    expect(output.supplemental_content).toEqual({ recovered: true })
    expect(output.completion).toBe('interrupted')
  })

  it('separates supplemental data from identically named provider fields', () => {
    const content = { supplemental_content: 'provider field', _leapmux: { native: true } }
    const supplemental = { rawOutput: { text: 'Recovered result' } }
    const message = create(AgentChatMessageSchema, {
      content: new TextEncoder().encode(JSON.stringify(content)),
      contentCompression: ContentCompression.NONE,
      supplementalContent: new TextEncoder().encode(JSON.stringify(supplemental)),
      supplementalContentCompression: ContentCompression.NONE,
    })
    const out = JSON.parse(buildRawJsonEnvelope(message, parseMessageContent(message), 'agent'))
    expect(out.content).toEqual(content)
    expect(out.supplemental_content).toEqual(supplemental)
  })

  it('keeps recovered provider metadata separate from worker metadata', () => {
    const content = { metadata: { duration_ms: 'original provider field' } }
    const supplement = { provider: { metadata: { duration_ms: 'recovered provider field' } }, metadata: { duration_ms: 0 } }
    const message = create(AgentChatMessageSchema, {
      content: new TextEncoder().encode(JSON.stringify(content)),
      contentCompression: ContentCompression.NONE,
      supplementalContent: new TextEncoder().encode(JSON.stringify(supplement)),
      supplementalContentCompression: ContentCompression.NONE,
    })
    const parsed = parseMessageContent(message)
    const output = JSON.parse(buildRawJsonEnvelope(message, parsed, 'agent'))
    expect(output.content).toEqual(content)
    expect(output.supplemental_content).toEqual(supplement)
    expect(parsed.supplementalContent).toEqual(supplement.provider)
    expect(parsed.messageMetadata).toEqual({ duration_ms: 0 })
  })

  it('preserves JSON null as supplemental data', () => {
    const message = create(AgentChatMessageSchema, {
      content: new TextEncoder().encode('{}'),
      contentCompression: ContentCompression.NONE,
      supplementalContent: new TextEncoder().encode('null'),
      supplementalContentCompression: ContentCompression.NONE,
    })
    const out = JSON.parse(buildRawJsonEnvelope(message, parseMessageContent(message), 'agent'))
    expect(out.supplemental_content).toBeNull()
  })

  it('builds the envelope with the core fields and parsed content', () => {
    const out = JSON.parse(buildRawJsonEnvelope(msg(), parsed({ rawText: '{"hello":"world"}' }), 'user'))
    expect(out).toMatchObject({ id: 'm1', source: 'user', seq: 7, created_at: 'T', content: { hello: 'world' } })
  })

  it('serializes unsafe int64 seq values as exact strings', () => {
    const out = JSON.parse(buildRawJsonEnvelope(
      create(AgentChatMessageSchema, { id: 'm1', source: MessageSource.USER, seq: 9007199254740993n, createdAt: 'T' }),
      parsed({ rawText: '{}' }),
      'user',
    ))
    expect(out.seq).toBe('9007199254740993')
  })

  it('omits proto3 zero-value fields and includes the set ones', () => {
    const out = JSON.parse(buildRawJsonEnvelope(
      msg({ spanId: 's1', spanType: 'tool_use', depth: 2 }),
      parsed({ rawText: '{}' }),
      'agent',
    ))
    expect(out.span_id).toBe('s1')
    expect(out.span_type).toBe('tool_use')
    expect(out.depth).toBe(2)
    // unset optional fields are absent
    expect('parent_span_id' in out).toBe(false)
    expect('span_color' in out).toBe(false)
  })

  it('degrades a corrupt span_lines to its raw string instead of throwing', () => {
    const out = JSON.parse(buildRawJsonEnvelope(msg({ spanLines: '{not json' }), parsed({ rawText: '{}' }), 'user'))
    expect(out.span_lines).toBe('{not json')
  })

  it('parses a valid span_lines into structured JSON', () => {
    const out = JSON.parse(buildRawJsonEnvelope(msg({ spanLines: '[{"a":1}]' }), parsed({ rawText: '{}' }), 'user'))
    expect(out.span_lines).toEqual([{ a: 1 }])
  })

  it('keeps non-JSON content as a string in the envelope', () => {
    expect(JSON.parse(buildRawJsonEnvelope(msg(), parsed({ rawText: 'not json at all' }), 'user')).content).toBe('not json at all')
  })

  it('preserves the original notification wrapper inside content', () => {
    const out = JSON.parse(buildRawJsonEnvelope(
      msg(),
      parsed({ rawText: '{"type":"notification_thread","old_seqs":[3,4],"messages":[{"x":1}]}', wrapper: { old_seqs: [3, 4], messages: [{ x: 1 }] } }),
      'leapmux',
    ))
    expect(out.content.messages).toEqual([{ x: 1 }])
    expect(out.content.old_seqs).toEqual([3, 4])
    expect(out.content.type).toBe('notification_thread')
  })

  describe('geometry.height debug field', () => {
    it('emits the measured DOM height when present', () => {
      const out = JSON.parse(buildRawJsonEnvelope(
        msg(),
        parsed({ rawText: '{}' }),
        'agent',
        { measured: 203 },
      ))
      expect(out.geometry.height).toBe(203)
    })

    it('injects geometry on the wrapper/notification path too (not only the content path)', () => {
      const out = JSON.parse(buildRawJsonEnvelope(
        msg(),
        parsed({ rawText: '{"type":"notification_thread","old_seqs":[],"messages":[{"x":1}]}', wrapper: { old_seqs: [], messages: [{ x: 1 }] } }),
        'leapmux',
        { measured: 203 },
      ))
      // Geometry stays outside the original notification content.
      expect(out.content.messages).toEqual([{ x: 1 }])
      expect(out.geometry.height).toBe(203)
    })

    it('omits geometry entirely when heights is undefined or empty', () => {
      const noArg = JSON.parse(buildRawJsonEnvelope(msg(), parsed({ rawText: '{}' }), 'agent'))
      expect('geometry' in noArg).toBe(false)
      const empty = JSON.parse(buildRawJsonEnvelope(msg(), parsed({ rawText: '{}' }), 'agent', {}))
      expect('geometry' in empty).toBe(false)
    })
  })
})
