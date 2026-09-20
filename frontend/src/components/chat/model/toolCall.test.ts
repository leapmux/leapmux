import type { ToolCallFault, ToolCallLifecycleFacts } from './toolCall'
import type { ToolCallStatus } from './toolCallStatus'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { __resetToolCallWarningsForTest, createToolCall } from './createToolCall'
import { failedResult, isToolFailureResult, isUnparsedToolResult, proseResult, typedResult, unparsedResult } from './toolCall'
import { deriveToolCallStatus } from './toolCallLifecycle'
import { FINISHED_TOOL_STATUSES, isFinishedToolCallStatus, UNFINISHED_TOOL_STATUSES } from './toolCallStatus'
import { TOOL_KINDS } from './toolKind'
import { NO_PAYLOAD_RESERVES_A_BRAND, REQUESTS_COVER_TOOL_KINDS, RESULTS_COVER_TOOL_KINDS } from './tools'

/** An envelope whose frame states exactly a status word and nothing else. */
function TEST_LIFECYCLE(frameStatus: ToolCallStatus): ToolCallLifecycleFacts {
  return {
    frameStatus,
    providerOutcome: null,
    retainedOutcome: null,
    rowFinal: false,
    resultFrameLanded: false,
  }
}

describe('the kind-discriminated tool call model', () => {
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
    expect(isUnparsedToolResult(unparsedResult('raw'))).toBe(true)
    expect(isToolFailureResult(failedResult('boom'))).toBe(true)
    expect(isUnparsedToolResult(failedResult('boom'))).toBe(false)
    expect(isToolFailureResult(unparsedResult('raw'))).toBe(false)
    expect(isUnparsedToolResult(proseResult('words'))).toBe(false)
  })

  it('answers the typed result only for the kind\'s own payload', () => {
    expect(typedResult({ kind: 'think', result: proseResult('thought') })?.text).toBe('thought')
    expect(typedResult({ kind: 'think', result: failedResult('no') })).toBeUndefined()
    expect(typedResult({ kind: 'think', result: unparsedResult('raw') })).toBeUndefined()
    expect(typedResult({ kind: 'think' })).toBeUndefined()
  })

  it('joins an envelope with a payload and defaults the images to none', () => {
    const call = createToolCall({ id: 'f1', name: 'WebFetch', lifecycle: { ...TEST_LIFECYCLE('unstated'), rowFinal: true } }, { kind: 'fetch', request: { url: 'https://example.com' } })
    expect(call.id).toBe('f1')
    expect(call.name).toBe('WebFetch')
    expect(call.status).toBe('incomplete')
    expect(call.images).toEqual([])
    expect(call).not.toHaveProperty('result')
  })

  // `exactOptionalPropertyTypes` used to be off, so a payload that states a key as
  // `undefined` is a key that EXISTS. Both of these are the natural spelling of "this
  // frame carries none", and spreading them over the envelope erased what the envelope
  // knew: the name the vocabulary tests walk, and the empty list `imagesForRow` spreads
  // (which threw on `undefined` and took the whole row list with it). The wire hands a
  // provider a loose record, so the fixture states its absent halves that way and the
  // spread carries the keys onto the payload verbatim -- the typed shape cannot spell
  // them, and the join must read a stated key the same as an absent one.
  it('keeps the envelope fields a payload states as undefined', () => {
    const statedNone: Record<string, unknown> = { name: undefined, images: undefined }
    const call = createToolCall(
      { id: 'c4', name: 'Bash', lifecycle: TEST_LIFECYCLE('completed') },
      { kind: 'execute', request: { command: 'ls' }, ...statedNone },
    )
    expect(call.name).toBe('Bash')
    expect(call.images).toEqual([])
  })

  it('lets a payload that states a name of its own win', () => {
    const call = createToolCall(
      { id: 'c5', name: '', lifecycle: TEST_LIFECYCLE('completed') },
      { kind: 'execute', request: { command: 'ls' }, name: 'run_terminal_cmd' },
    )
    expect(call.name).toBe('run_terminal_cmd')
  })

  // The body a provider read knows an outcome the envelope's own status cannot
  // state: a frame that says `completed` over a result that failed, or a plan the
  // reader refused arriving as an error. Every provider used to apply this itself.
  it('lets the payload state the outcome the envelope could not', () => {
    const call = createToolCall(
      { id: 'c1', name: 'ExitPlanMode', lifecycle: TEST_LIFECYCLE('completed') },
      { kind: 'switch_mode', request: { mode: 'plan' }, statusOverride: 'declined', result: proseResult('Not yet') },
    )
    expect(call.status).toBe('declined')
  })

  it('keeps the envelope status when the payload states no override', () => {
    const call = createToolCall({ id: 'c2', name: 'Bash', lifecycle: { frameStatus: 'failed', providerOutcome: null, retainedOutcome: null, rowFinal: false, resultFrameLanded: false } }, { kind: 'execute', request: { command: 'false' } })
    expect(call.status).toBe('failed')
  })

  // `status` is the ONE outcome word a row carries, so the override must not
  // survive onto the call as a second one.
  it('leaves the override off the built call', () => {
    const call = createToolCall(
      { id: 'c3', name: 'Bash', lifecycle: TEST_LIFECYCLE('completed') },
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
  const FAULT_DRAFTS: Array<[ToolCallFault, Parameters<typeof createToolCall>]> = [
    ['result-before-the-call-finished', [{ id: 'f1', name: 'Think', lifecycle: TEST_LIFECYCLE('pending') }, { kind: 'think', request: { text: 't' }, result: proseResult('early') }]],
    ['pictures-before-the-call-finished', [{ id: 'f2', name: 'Read', lifecycle: TEST_LIFECYCLE('in_progress') }, { kind: 'read', request: { path: '/a' }, images: [{ mimeType: 'image/png', data: 'aGk=' }] }]],
    ['completed-with-a-failure-result', [{ id: 'f4', name: 'Think', lifecycle: TEST_LIFECYCLE('completed') }, { kind: 'think', request: { text: 't' }, result: failedResult('boom') }]],
    ['failed-with-an-unparsed-result', [{ id: 'f5', name: 'Read', lifecycle: TEST_LIFECYCLE('failed') }, { kind: 'read', request: { path: '/a' }, result: unparsedResult('raw') }]],
    ['declined-with-a-typed-payload', [{ id: 'f6', name: 'Read', lifecycle: TEST_LIFECYCLE('declined') }, { kind: 'read', request: { path: '/a' }, result: { lines: null, fallbackContent: 'body' } }]],
    ['a-generic-kind-carries-its-own-images', [{ id: 'f7', name: 'Tool', lifecycle: TEST_LIFECYCLE('completed') }, { kind: 'mcp', request: { server: 's', tool: 't', args: {} }, images: [{ mimeType: 'image/png', data: 'aGk=' }], result: { content: [] } }]],
    ['a-file-change-states-no-file', [{ id: 'f8', name: 'Edit', lifecycle: TEST_LIFECYCLE('completed') }, { kind: 'edit', request: { changes: [] }, result: { changes: [] } }]],
  ]

  it('preserves the fault and the original kind on the degraded call', () => {
    for (const [fault, [envelope, payload]] of FAULT_DRAFTS) {
      const call = createToolCall(envelope, payload)
      expect(call.kind, fault).toBe('other')
      expect(call.degradation, fault).toEqual({ fault, originalKind: payload.kind })
    }
  })

  it('warns once per fault code, however many frames break it', () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    for (let i = 0; i < 3; i++) {
      createToolCall({ id: 'f2', name: 'Read', lifecycle: TEST_LIFECYCLE('in_progress') }, { kind: 'read', request: { path: '/a' }, images: [{ mimeType: 'image/png', data: 'aGk=' }] })
    }
    expect(warn).toHaveBeenCalledTimes(1)
    expect(warn.mock.calls[0]?.[1]).toMatchObject({ fault: 'pictures-before-the-call-finished', callId: 'f2', toolName: 'Read', originalKind: 'read', status: 'in_progress' })
  })

  it('warns separately for each fault code', () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    for (const [fault, [envelope, payload]] of FAULT_DRAFTS) {
      createToolCall(envelope, payload)
      expect(warn, fault).toHaveBeenCalledTimes([...FAULT_DRAFTS].findIndex(([code]) => code === fault) + 1)
    }
    // The cap is the closed fault union: one report per member and no more.
    expect(warn).toHaveBeenCalledTimes(FAULT_DRAFTS.length)
  })

  it('restores the warnings after a reset', () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    createToolCall({ id: 'f2', name: 'Read', lifecycle: TEST_LIFECYCLE('in_progress') }, { kind: 'read', request: { path: '/a' }, images: [{ mimeType: 'image/png', data: 'aGk=' }] })
    expect(warn).toHaveBeenCalledTimes(1)
    __resetToolCallWarningsForTest()
    createToolCall({ id: 'f2', name: 'Read', lifecycle: TEST_LIFECYCLE('in_progress') }, { kind: 'read', request: { path: '/a' }, images: [{ mimeType: 'image/png', data: 'aGk=' }] })
    expect(warn).toHaveBeenCalledTimes(2)
  })

  it('states no degradation on a valid generic call', () => {
    const call = createToolCall({ id: 'g', name: 'Tool', lifecycle: { frameStatus: 'completed', providerOutcome: null, retainedOutcome: null, rowFinal: false, resultFrameLanded: false } }, { kind: 'other', request: { args: { a: 1 } }, result: { content: [{ type: 'text', text: 'ok' }] } })
    expect(call.kind).toBe('other')
    expect(call.degradation).toBeUndefined()
  })
})

// The ONE derivation of a call's status, over the whole fact space: every frame
// status, each provider outcome, each retained outcome, both resultFrameLanded values,
// and each finished override. The expected answer is spelled from the RULES the
// derivation's own doc states -- an independent reading, not a second call.
describe('deriveToolCallStatus', () => {
  const PROVIDER_OUTCOMES = [null, 'succeeded', 'failed', 'interrupted', 'declined'] as const
  const RETAINED_OUTCOMES = [null, 'succeeded', 'failed', 'interrupted'] as const

  /** The rules, restated independently of the function under test. */
  function expected(facts: ToolCallLifecycleFacts, resultAvailable: boolean, statusOverride?: 'completed' | 'failed' | 'cancelled' | 'declined'): ToolCallStatus {
    if (statusOverride !== undefined)
      return statusOverride === 'completed' && !resultAvailable ? 'incomplete' : statusOverride
    if (facts.providerOutcome === 'interrupted' || facts.retainedOutcome === 'interrupted')
      return 'cancelled'
    if (facts.providerOutcome === 'failed')
      return 'failed'
    if (facts.providerOutcome === 'declined')
      return 'declined'
    if (isFinishedToolCallStatus(facts.frameStatus))
      return facts.frameStatus === 'completed' && !resultAvailable ? 'incomplete' : facts.frameStatus
    if (facts.retainedOutcome === 'failed')
      return 'failed'
    if (facts.resultFrameLanded)
      return resultAvailable ? 'completed' : 'incomplete'
    if (facts.rowFinal)
      return resultAvailable ? 'completed' : 'incomplete'
    return facts.frameStatus
  }

  it('derives every combination of the fact space from the stated precedence', () => {
    for (const frameStatus of [...UNFINISHED_TOOL_STATUSES, ...FINISHED_TOOL_STATUSES]) {
      for (const providerOutcome of PROVIDER_OUTCOMES) {
        for (const retainedOutcome of RETAINED_OUTCOMES) {
          for (const rowFinal of [false, true]) {
            for (const resultFrameLanded of [false, true]) {
              for (const resultAvailable of [false, true]) {
                const facts: ToolCallLifecycleFacts = { frameStatus, providerOutcome, retainedOutcome, rowFinal, resultFrameLanded }
                expect(deriveToolCallStatus(facts, resultAvailable), JSON.stringify({ facts, resultAvailable })).toBe(expected(facts, resultAvailable))
              }
            }
          }
        }
      }
    }
  })

  it('lets each finished override win over every fact', () => {
    for (const statusOverride of ['completed', 'failed', 'cancelled', 'declined'] as const) {
      for (const frameStatus of [...UNFINISHED_TOOL_STATUSES, ...FINISHED_TOOL_STATUSES]) {
        const facts: ToolCallLifecycleFacts = { frameStatus, providerOutcome: 'failed', retainedOutcome: 'interrupted', rowFinal: true, resultFrameLanded: true }
        expect(deriveToolCallStatus(facts, true, statusOverride), `${frameStatus} + ${statusOverride}`).toBe(statusOverride)
      }
    }
  })

  // The one rule the table above cannot show on its own: a SUCCEEDED outcome
  // completes nothing. A turn that later stopped is not a tool that finished, and
  // only the landed result states the second fact.
  it('keeps a succeeded outcome from completing an unfinished, unanswered call', () => {
    expect(deriveToolCallStatus({ frameStatus: 'in_progress', providerOutcome: 'succeeded', retainedOutcome: 'succeeded', rowFinal: false, resultFrameLanded: false }, false)).toBe('in_progress')
    expect(deriveToolCallStatus({ frameStatus: 'unstated', providerOutcome: null, retainedOutcome: 'succeeded', rowFinal: false, resultFrameLanded: false }, false)).toBe('unstated')
    // The landed result is what completes it.
    expect(deriveToolCallStatus({ frameStatus: 'in_progress', providerOutcome: 'succeeded', retainedOutcome: 'succeeded', rowFinal: false, resultFrameLanded: true }, true)).toBe('completed')
  })

  it('keeps a provider body outcome over an explicit finished frame word', () => {
    expect(deriveToolCallStatus({ frameStatus: 'completed', providerOutcome: 'failed', retainedOutcome: null, rowFinal: true, resultFrameLanded: true }, true)).toBe('failed')
    expect(deriveToolCallStatus({ frameStatus: 'failed', providerOutcome: 'interrupted', retainedOutcome: null, rowFinal: true, resultFrameLanded: true }, true)).toBe('cancelled')
  })

  it('lets a retained interruption win over a provider failure', () => {
    expect(deriveToolCallStatus({ frameStatus: 'completed', providerOutcome: 'failed', retainedOutcome: 'interrupted', rowFinal: true, resultFrameLanded: true }, true)).toBe('cancelled')
  })
})

/**
 * The illegal status/result pairs live in `toolCall.typecheck.ts`, a compile-only
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
    expect(isUnparsedToolResult({ unparsed: true })).toBe(false)
    expect(isToolFailureResult({ failure: true })).toBe(false)
    expect(isUnparsedToolResult({ unparsed: true, text: 7 })).toBe(false)
    expect(isToolFailureResult({ failure: true, text: null })).toBe(false)
  })

  it('accepts the shape its own factory builds', () => {
    expect(isUnparsedToolResult(unparsedResult('raw'))).toBe(true)
    expect(isToolFailureResult(failedResult('boom'))).toBe(true)
    expect(isUnparsedToolResult({ unparsed: true, text: '' })).toBe(true)
  })

  // The VALUE, not the key: `{ unparsed: false }` is a payload, not a brand.
  it('refuses a false brand', () => {
    expect(isUnparsedToolResult({ unparsed: false, text: 'x' })).toBe(false)
    expect(isToolFailureResult({ failure: false, text: 'x' })).toBe(false)
  })

  it('refuses what is not an object at all', () => {
    for (const value of [null, undefined, 'text', 7, []])
      expect(isUnparsedToolResult(value)).toBe(false)
  })
})
