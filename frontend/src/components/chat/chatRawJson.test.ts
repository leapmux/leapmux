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
    it('emits estimated, measured, and signed delta/delta_pct when both are present', () => {
      const out = JSON.parse(buildRawJsonEnvelope(
        msg(),
        parsed({ rawText: '{}' }),
        'agent',
        { estimated: 184, measured: 203 },
      ))
      expect(out.geometry.height.estimated).toBe(184)
      expect(out.geometry.height.measured).toBe(203)
      expect(out.geometry.height.delta).toBe(19) // measured - estimated, positive => under-estimate
      expect(out.geometry.height.delta_pct).toBeCloseTo(19 / 203, 6)
    })

    it('nulls the missing side and omits delta/delta_pct when only the estimate is known', () => {
      const out = JSON.parse(buildRawJsonEnvelope(msg(), parsed({ rawText: '{}' }), 'agent', { estimated: 184 }))
      expect(out.geometry.height).toEqual({ estimated: 184, measured: null })
    })

    it('nulls the missing side and omits delta/delta_pct when only the measurement is known', () => {
      const out = JSON.parse(buildRawJsonEnvelope(msg(), parsed({ rawText: '{}' }), 'agent', { measured: 203 }))
      expect(out.geometry.height).toEqual({ estimated: null, measured: 203 })
    })

    it('uses delta_pct 0 (not NaN/Infinity) when the measured height is 0', () => {
      const out = JSON.parse(buildRawJsonEnvelope(msg(), parsed({ rawText: '{}' }), 'agent', { estimated: 50, measured: 0 }))
      expect(out.geometry.height.delta).toBe(-50)
      expect(out.geometry.height.delta_pct).toBe(0)
    })

    it('reports a negative delta/delta_pct for an over-estimate (estimated > measured)', () => {
      const out = JSON.parse(buildRawJsonEnvelope(msg(), parsed({ rawText: '{}' }), 'agent', { estimated: 200, measured: 150 }))
      expect(out.geometry.height.delta).toBe(-50)
      expect(out.geometry.height.delta_pct).toBeCloseTo(-50 / 150, 6)
    })

    it('injects geometry on the wrapper/notification path too (not only the content path)', () => {
      const out = JSON.parse(buildRawJsonEnvelope(
        msg(),
        parsed({ rawText: '{"ignored":true}', wrapper: { old_seqs: [], messages: [{ x: 1 }] } }),
        'leapmux',
        { estimated: 184, measured: 203 },
      ))
      // The geometry field sits before the early-returning wrapper branch, so it
      // coexists with messages and is not swallowed by the content-path return.
      expect(out.messages).toEqual([{ x: 1 }])
      expect(out.geometry.height).toMatchObject({ estimated: 184, measured: 203, delta: 19 })
    })

    it('includes the estimate breakdown under geometry.height.breakdown when provided', () => {
      const breakdown = { kind: 'tool_result', total: 54, terms: [{ label: 'collapsed body', value: 54 }], metrics: { collapsed: true } }
      const out = JSON.parse(buildRawJsonEnvelope(msg(), parsed({ rawText: '{}' }), 'agent', { estimated: 54, measured: 80, breakdown }))
      expect(out.geometry.height.breakdown).toEqual(breakdown)
      expect(out.geometry.height.estimated).toBe(54)
    })

    it('omits breakdown when not provided', () => {
      const out = JSON.parse(buildRawJsonEnvelope(msg(), parsed({ rawText: '{}' }), 'agent', { estimated: 54, measured: 80 }))
      expect('breakdown' in out.geometry.height).toBe(false)
    })

    it('omits geometry entirely when heights is undefined or empty', () => {
      const noArg = JSON.parse(buildRawJsonEnvelope(msg(), parsed({ rawText: '{}' }), 'agent'))
      expect('geometry' in noArg).toBe(false)
      const empty = JSON.parse(buildRawJsonEnvelope(msg(), parsed({ rawText: '{}' }), 'agent', {}))
      expect('geometry' in empty).toBe(false)
    })
  })
})
