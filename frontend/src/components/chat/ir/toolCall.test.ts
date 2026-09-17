import type { ReadFileResult } from './readFileResult'
import type { ToolCallOf } from './toolCall'
import type { ImageResultSource } from '~/lib/imageBlocks'
import { describe, expect, it } from 'vitest'
import { failedResult, isFailedResult, isUnparsedResult, proseResult, toolCall, typedResult, unparsedResult } from './toolCall'
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

/**
 * The ILLEGAL status/result pairs, refused by the compiler instead of a runtime walk.
 *
 * Every `@ts-expect-error` below is the test: the lifecycle union states each rule as
 * a member that does not exist, so a pair the rule refuses fails to compile here
 * first -- and if a refactor loosens one, its directive stops matching an error and
 * `tsc` fails on THIS line rather than in the provider that quietly builds the pair.
 * Each block keeps the legal counterpart beside it, so a directive cannot pass by
 * refusing the legal pairs too.
 */
describe('the lifecycle types refuse the illegal status/result pairs', () => {
  const think = { text: 'thought' }
  const readResult: ReadFileResult = { lines: null, fallbackContent: 'body' }
  const picture: ImageResultSource = { mimeType: 'image/png', data: 'aGk=' }

  it('refuses the result of a call that has not answered (I1)', () => {
    const queued: ToolCallOf<'think'> = { id: 'q', name: 'Think', kind: 'think', request: think, status: 'pending', images: [] }
    expect(queued.status).toBe('pending')
    // @ts-expect-error I1: '' | 'pending' | 'in_progress' pair with no result.
    const early: ToolCallOf<'think'> = { id: 'q', name: 'Think', kind: 'think', request: think, status: 'in_progress', images: [], result: proseResult('early') }
    void early
  })

  it('requires a completed call to carry a typed or unparsed result, never a failure (I2)', () => {
    const done: ToolCallOf<'think'> = { id: 'd', name: 'Think', kind: 'think', request: think, status: 'completed', images: [], result: proseResult('done') }
    expect(done.result.text).toBe('done')
    // @ts-expect-error I2: 'completed' requires a result.
    const silent: ToolCallOf<'think'> = { id: 'd', name: 'Think', kind: 'think', request: think, status: 'completed', images: [] }
    void silent
    // @ts-expect-error I2: a completed call did not fail, so the failure brand is not its result.
    const failedDone: ToolCallOf<'think'> = { id: 'd', name: 'Think', kind: 'think', request: think, status: 'completed', images: [], result: failedResult('boom') }
    void failedDone
  })

  it('refuses the unparsed payload of a failed call (I4)', () => {
    const failed: ToolCallOf<'read'> = { id: 'f', name: 'Read', kind: 'read', request: { path: '/a' }, status: 'failed', images: [], result: failedResult('boom') }
    expect(failed.status).toBe('failed')
    const failedWithRecord: ToolCallOf<'read'> = { id: 'f', name: 'Read', kind: 'read', request: { path: '/a' }, status: 'failed', images: [], result: readResult }
    expect(failedWithRecord.status).toBe('failed')
    // @ts-expect-error I4: the unparsed brand states the call completed, which a failed call did not.
    const failedUnparsed: ToolCallOf<'read'> = { id: 'f', name: 'Read', kind: 'read', request: { path: '/a' }, status: 'failed', images: [], result: unparsedResult('raw') }
    void failedUnparsed
  })

  it('a declined call carries words or a failure, never a produced payload', () => {
    const declinedWords: ToolCallOf<'switch_mode'> = { id: 'x', name: 'ExitPlanMode', kind: 'switch_mode', request: { mode: 'plan' }, status: 'declined', images: [], result: proseResult('Not yet') }
    expect(declinedWords.status).toBe('declined')
    const declinedFailure: ToolCallOf<'read'> = { id: 'x', name: 'Read', kind: 'read', request: { path: '/a' }, status: 'declined', images: [], result: failedResult('refused') }
    expect(declinedFailure.status).toBe('declined')
    // @ts-expect-error Declined: a read that never ran produced no file body.
    const declinedRead: ToolCallOf<'read'> = { id: 'x', name: 'Read', kind: 'read', request: { path: '/a' }, status: 'declined', images: [], result: readResult }
    void declinedRead
    // @ts-expect-error Declined: the unparsed brand states the call completed, which a refused one did not.
    const declinedUnparsed: ToolCallOf<'read'> = { id: 'x', name: 'Read', kind: 'read', request: { path: '/a' }, status: 'declined', images: [], result: unparsedResult('raw') }
    void declinedUnparsed
  })

  it('a cancelled call keeps whatever partial body it printed, in any of the three shapes', () => {
    const partialTyped: ToolCallOf<'read'> = { id: 'c', name: 'Read', kind: 'read', request: { path: '/a' }, status: 'cancelled', images: [], result: readResult }
    const partialFailed: ToolCallOf<'read'> = { id: 'c', name: 'Read', kind: 'read', request: { path: '/a' }, status: 'cancelled', images: [], result: failedResult('cut') }
    const partialUnparsed: ToolCallOf<'read'> = { id: 'c', name: 'Read', kind: 'read', request: { path: '/a' }, status: 'cancelled', images: [], result: unparsedResult('raw') }
    expect([partialTyped, partialFailed, partialUnparsed].every(call => call.status === 'cancelled')).toBe(true)
  })

  it('refuses the own pictures of a generic kind (I6)', () => {
    const bare: ToolCallOf<'mcp'> = { id: 'g', name: 'Tool', kind: 'mcp', request: { server: 's', tool: 't', args: {} }, status: 'completed', images: [], result: { content: [] } }
    expect(bare.images).toEqual([])
    const pictured: ToolCallOf<'read'> = { id: 'g', name: 'Read', kind: 'read', request: { path: '/a' }, status: 'completed', images: [picture], result: readResult }
    expect(pictured.images).toHaveLength(1)
    // @ts-expect-error I6: a generic kind's pictures ride in its result content, never on the call.
    const withPictures: ToolCallOf<'mcp'> = { id: 'g', name: 'Tool', kind: 'mcp', request: { server: 's', tool: 't', args: {} }, status: 'completed', images: [picture], result: { content: [] } }
    void withPictures
  })
})

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
