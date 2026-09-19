import type { ToolCallFault } from './toolCall'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { __resetToolCallWarningsForTest, failedResult, isFailedResult, isUnparsedResult, proseResult, toolCall, typedResult, unparsedResult } from './toolCall'
import { TOOL_KINDS } from './toolKind'
import { NO_PAYLOAD_RESERVES_A_BRAND, REQUESTS_COVER_TOOL_KINDS, RESULTS_COVER_TOOL_KINDS } from './tools'

describe('the kind-discriminated tool call IR', () => {
  it('covers every tool kind in both request and result tables', () => {
    expect(REQUESTS_COVER_TOOL_KINDS).toBe(true)
    expect(RESULTS_COVER_TOOL_KINDS).toBe(true)
    expect(NO_PAYLOAD_RESERVES_A_BRAND).toBe(true)
  })

  it('adds web_search and question to the kind set', () => {
    expect(TOOL_KINDS).toContain('web_search')
    expect(TOOL_KINDS).toContain('question')
    expect(TOOL_KINDS).toHaveLength(30)
  })

  it('narrows each of the four result states', () => {
    expect(isUnparsedResult(unparsedResult('raw'))).toBe(true)
    expect(isFailedResult(failedResult('boom'))).toBe(true)
    expect(isUnparsedResult(failedResult('boom'))).toBe(false)
    expect(isFailedResult(unparsedResult('raw'))).toBe(false)
    expect(isUnparsedResult(proseResult('words'))).toBe(false)
  })

  it('answers the typed result only for the kind\'s own payload', () => {
    expect(typedResult({ kind: 'think', result: proseResult('thought') })?.text).toBe('thought')
    expect(typedResult({ kind: 'think', result: failedResult('no') })).toBeUndefined()
    expect(typedResult({ kind: 'think', result: unparsedResult('raw') })).toBeUndefined()
    expect(typedResult({ kind: 'think' })).toBeUndefined()
  })

  it('joins an envelope with a payload and defaults the images to none', () => {
    const call = toolCall({ id: 'f1', name: 'WebFetch', status: 'completed' }, { kind: 'fetch', request: { url: 'https://example.com' } })
    expect(call.id).toBe('f1')
    expect(call.name).toBe('WebFetch')
    expect(call.status).toBe('completed')
    expect(call.images).toEqual([])
  })

  // `exactOptionalPropertyTypes` used to be off, so a payload that states a key as
  // `undefined` is a key that EXISTS. Both of these are the natural spelling of "this
  // frame carries none", and spreading them over the envelope erased what the envelope
  // knew: the name the vocabulary tests walk, and the empty list `imagesForIR` spreads
  // (which threw on `undefined` and took the whole row list with it). The wire hands a
  // provider a loose record, so the fixture states its absent halves that way and the
  // spread carries the keys onto the payload verbatim -- the typed shape cannot spell
  // them, and the join must read a stated key the same as an absent one.
  it('keeps the envelope fields a payload states as undefined', () => {
    const statedNone: Record<string, unknown> = { name: undefined, images: undefined }
    const call = toolCall(
      { id: 'c4', name: 'Bash', status: 'completed' },
      { kind: 'execute', request: { command: 'ls' }, ...statedNone },
    )
    expect(call.name).toBe('Bash')
    expect(call.images).toEqual([])
  })

  it('lets a payload that states a name of its own win', () => {
    const call = toolCall(
      { id: 'c5', name: '', status: 'completed' },
      { kind: 'execute', request: { command: 'ls' }, name: 'run_terminal_cmd' },
    )
    expect(call.name).toBe('run_terminal_cmd')
  })

  // The body a provider read knows an outcome the envelope's own status cannot
  // state: a frame that says `completed` over a result that failed, or a plan the
  // reader refused arriving as an error. Every provider used to apply this itself.
  it('lets the payload state the outcome the envelope could not', () => {
    const call = toolCall(
      { id: 'c1', name: 'ExitPlanMode', status: 'completed' },
      { kind: 'switch_mode', request: { mode: 'plan' }, statusOverride: 'declined', result: proseResult('Not yet') },
    )
    expect(call.status).toBe('declined')
  })

  it('keeps the envelope status when the payload states no override', () => {
    const call = toolCall({ id: 'c2', name: 'Bash', status: 'failed' }, { kind: 'execute', request: { command: 'false' } })
    expect(call.status).toBe('failed')
  })

  // `status` is the ONE outcome word a row carries, so the override must not
  // survive onto the call as a second one.
  it('leaves the override off the built call', () => {
    const call = toolCall(
      { id: 'c3', name: 'Bash', status: 'completed' },
      { kind: 'execute', request: { command: 'false' }, statusOverride: 'failed' },
    )
    expect(call).not.toHaveProperty('statusOverride')
    expect(Object.keys(call)).not.toContain('statusOverride')
  })
})

// A malformed draft remains renderable -- the degrade is the row that draws --
// but the degrade is OBSERVABLE: the call states which invariant broke and which
// kind it was reading, and the reporter warns once per fault code for the operator
// watching a live session. The census tests read the METADATA, never the warnings.
describe('an observable degradation', () => {
  afterEach(() => {
    __resetToolCallWarningsForTest()
    vi.restoreAllMocks()
  })

  /** One draft per fault code, each named by the invariant it breaks. */
  const FAULT_DRAFTS: Array<[ToolCallFault, Parameters<typeof toolCall>]> = [
    ['result-before-the-call-finished', [{ id: 'f1', name: 'Think', status: 'pending' }, { kind: 'think', request: { text: 't' }, result: proseResult('early') }]],
    ['pictures-before-the-call-finished', [{ id: 'f2', name: 'Read', status: 'in_progress' }, { kind: 'read', request: { path: '/a' }, images: [{ mimeType: 'image/png', data: 'aGk=' }] }]],
    ['completed-with-no-result', [{ id: 'f3', name: 'Bash', status: 'completed' }, { kind: 'execute', request: { command: 'ls' } }]],
    ['completed-with-a-failure-result', [{ id: 'f4', name: 'Think', status: 'completed' }, { kind: 'think', request: { text: 't' }, result: failedResult('boom') }]],
    ['failed-with-an-unparsed-result', [{ id: 'f5', name: 'Read', status: 'failed' }, { kind: 'read', request: { path: '/a' }, result: unparsedResult('raw') }]],
    ['declined-with-a-typed-payload', [{ id: 'f6', name: 'Read', status: 'declined' }, { kind: 'read', request: { path: '/a' }, result: { lines: null, fallbackContent: 'body' } }]],
    ['a-generic-kind-carries-its-own-images', [{ id: 'f7', name: 'Tool', status: 'completed' }, { kind: 'mcp', request: { server: 's', tool: 't', args: {} }, images: [{ mimeType: 'image/png', data: 'aGk=' }], result: { content: [] } }]],
    ['a-file-change-states-no-file', [{ id: 'f8', name: 'Edit', status: 'completed' }, { kind: 'edit', request: { changes: [] }, result: { changes: [] } }]],
  ]

  it('preserves the fault and the original kind on the degraded call', () => {
    for (const [fault, [envelope, payload]] of FAULT_DRAFTS) {
      const call = toolCall(envelope, payload)
      expect(call.kind, fault).toBe('other')
      expect(call.degradation, fault).toEqual({ fault, originalKind: payload.kind })
    }
  })

  it('warns once per fault code, however many frames break it', () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    for (let i = 0; i < 3; i++) {
      toolCall({ id: 'f3', name: 'Bash', status: 'completed' }, { kind: 'execute', request: { command: 'ls' } })
    }
    expect(warn).toHaveBeenCalledTimes(1)
    expect(warn.mock.calls[0]?.[1]).toMatchObject({ fault: 'completed-with-no-result', callId: 'f3', toolName: 'Bash', originalKind: 'execute', status: 'completed' })
  })

  it('warns separately for each fault code', () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    for (const [fault, [envelope, payload]] of FAULT_DRAFTS) {
      toolCall(envelope, payload)
      expect(warn, fault).toHaveBeenCalledTimes([...FAULT_DRAFTS].findIndex(([code]) => code === fault) + 1)
    }
    // The cap is the closed fault union: one report per member and no more.
    expect(warn).toHaveBeenCalledTimes(FAULT_DRAFTS.length)
  })

  it('restores the warnings after a reset', () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    toolCall({ id: 'f3', name: 'Bash', status: 'completed' }, { kind: 'execute', request: { command: 'ls' } })
    expect(warn).toHaveBeenCalledTimes(1)
    __resetToolCallWarningsForTest()
    toolCall({ id: 'f3', name: 'Bash', status: 'completed' }, { kind: 'execute', request: { command: 'ls' } })
    expect(warn).toHaveBeenCalledTimes(2)
  })

  it('states no degradation on a valid generic call', () => {
    const call = toolCall({ id: 'g', name: 'Tool', status: 'completed' }, { kind: 'other', request: { args: { a: 1 } }, result: { content: [{ type: 'text', text: 'ok' }] } })
    expect(call.kind).toBe('other')
    expect(call.degradation).toBeUndefined()
  })
})

/**
 * The ILLEGAL status/result pairs live in `toolCall.typecheck.ts`, a compile-only
 * module under this directory: every rule is a `@ts-expect-error` the TypeScript
 * configurations read, so a lifecycle the union refuses fails `tsc` rather than
 * a runtime walk.
 */

/**
 * A predicate promises the WHOLE type, and every caller here reads `.text` with no
 * guard of its own. A brand-only test handed `hasMoreLinesThan` and `<PlainTextResult>`
 * an undefined string, which draws an empty body or throws in the collapse count.
 */
describe('branded result guards', () => {
  it('refuses a brand with no text', () => {
    expect(isUnparsedResult({ unparsed: true })).toBe(false)
    expect(isFailedResult({ failure: true })).toBe(false)
    expect(isUnparsedResult({ unparsed: true, text: 7 })).toBe(false)
    expect(isFailedResult({ failure: true, text: null })).toBe(false)
  })

  it('accepts the shape its own factory builds', () => {
    expect(isUnparsedResult(unparsedResult('raw'))).toBe(true)
    expect(isFailedResult(failedResult('boom'))).toBe(true)
    expect(isUnparsedResult({ unparsed: true, text: '' })).toBe(true)
  })

  // The VALUE, not the key: `{ unparsed: false }` is a payload, not a brand.
  it('refuses a false brand', () => {
    expect(isUnparsedResult({ unparsed: false, text: 'x' })).toBe(false)
    expect(isFailedResult({ failure: false, text: 'x' })).toBe(false)
  })

  it('refuses what is not an object at all', () => {
    for (const value of [null, undefined, 'text', 7, []])
      expect(isUnparsedResult(value)).toBe(false)
  })
})
