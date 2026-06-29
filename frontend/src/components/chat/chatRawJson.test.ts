import type { ParsedMessageContent } from '~/lib/messageParser'
import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { AgentChatMessageSchema, MessageSource } from '~/generated/leapmux/v1/agent_pb'
import { buildRawJsonEnvelope } from './chatRawJson'

interface MsgOver {
  spanId?: string
  spanType?: string
  parentSpanId?: string
  spanColor?: number
  spanLines?: string
  deliveryError?: string
  depth?: number
}

function msg(over: MsgOver = {}) {
  return create(AgentChatMessageSchema, { id: 'm1', source: MessageSource.USER, seq: 7n, createdAt: 'T', ...over })
}

function parsed(over: Partial<ParsedMessageContent> = {}): ParsedMessageContent {
  return { rawText: '', topLevel: null, parentObject: undefined, wrapper: null, ...over }
}

describe('buildRawJsonEnvelope', () => {
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
      msg({ spanId: 's1', spanType: 'tool_use', deliveryError: 'oops', depth: 2 }),
      parsed({ rawText: '{}' }),
      'agent',
    ))
    expect(out.span_id).toBe('s1')
    expect(out.span_type).toBe('tool_use')
    expect(out.delivery_error).toBe('oops')
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

  it('returns the raw text when content is not JSON', () => {
    expect(buildRawJsonEnvelope(msg(), parsed({ rawText: 'not json at all' }), 'user')).toBe('not json at all')
  })

  it('uses the wrapper messages/old_seqs for a notification thread and skips content', () => {
    const out = JSON.parse(buildRawJsonEnvelope(
      msg(),
      parsed({ rawText: '{"ignored":true}', wrapper: { old_seqs: [3, 4], messages: [{ x: 1 }] } }),
      'leapmux',
    ))
    expect(out.messages).toEqual([{ x: 1 }])
    expect(out.old_seqs).toEqual([3, 4])
    expect('content' in out).toBe(false)
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
        parsed({ rawText: '{"ignored":true}', wrapper: { old_seqs: [], messages: [{ x: 1 }] } }),
        'leapmux',
        { measured: 203 },
      ))
      // The geometry field sits before the early-returning wrapper branch, so it
      // coexists with messages and is not swallowed by the content-path return.
      expect(out.messages).toEqual([{ x: 1 }])
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
