import type { ReadFileResult } from './readFileResult'
import type { ToolCallIR, ToolCallOf, ToolCallPayload, ToolCallPayloadIR, ToolResultOf } from './toolCall'
import type { ToolKind } from './toolKind'
import type { ToolRequests } from './tools'
import type { ImageResultSource } from '~/lib/imageBlocks'
import { failedResult, proseResult, unparsedResult } from './toolCall'

// The ILLEGAL status/result pairs of the tool-call lifecycle, refused by the
// compiler instead of a runtime walk.
//
// Every `@ts-expect-error` below is the contract: the lifecycle union states
// each rule as a member that does not exist, so a pair the rule refuses fails
// to compile here first -- and if a refactor loosens one, its directive stops
// matching an error and `tsc` fails on THIS line rather than in the provider
// that quietly builds the pair. Each block keeps the legal counterpart beside
// it, so a directive cannot pass by refusing the legal pairs too.
//
// This module is compile-only: it belongs under `src` so both TypeScript
// configurations read it, no test runner executes it, and it exports nothing.

const think = { text: 'thought' }
const readResult: ReadFileResult = { lines: null, fallbackContent: 'body' }
const picture: ImageResultSource = { mimeType: 'image/png', data: 'aGk=' }

// I1: a call that has not answered carries no result.
const queued: ToolCallOf<'think'> = { id: 'q', name: 'Think', kind: 'think', request: think, status: 'pending', images: [] }
void queued
// @ts-expect-error I1: '' | 'pending' | 'in_progress' pair with no result.
const early: ToolCallOf<'think'> = { id: 'q', name: 'Think', kind: 'think', request: think, status: 'in_progress', images: [], result: proseResult('early') }
void early

// I2: 'completed' requires a typed or unparsed result, never a failure.
const done: ToolCallOf<'think'> = { id: 'd', name: 'Think', kind: 'think', request: think, status: 'completed', images: [], result: proseResult('done') }
void done
// @ts-expect-error I2: 'completed' requires a result.
const silent: ToolCallOf<'think'> = { id: 'd', name: 'Think', kind: 'think', request: think, status: 'completed', images: [] }
void silent
// @ts-expect-error I2: a completed call did not fail, so the failure brand is not its result.
const failedDone: ToolCallOf<'think'> = { id: 'd', name: 'Think', kind: 'think', request: think, status: 'completed', images: [], result: failedResult('boom') }
void failedDone

// I4: the unparsed brand states the call completed, which a failed call did not.
const failed: ToolCallOf<'read'> = { id: 'f', name: 'Read', kind: 'read', request: { path: '/a' }, status: 'failed', images: [], result: failedResult('boom') }
void failed
const failedWithRecord: ToolCallOf<'read'> = { id: 'f', name: 'Read', kind: 'read', request: { path: '/a' }, status: 'failed', images: [], result: readResult }
void failedWithRecord
// @ts-expect-error I4: the unparsed brand states the call completed, which a failed call did not.
const failedUnparsed: ToolCallOf<'read'> = { id: 'f', name: 'Read', kind: 'read', request: { path: '/a' }, status: 'failed', images: [], result: unparsedResult('raw') }
void failedUnparsed

// Declined: a refused call carries words or a failure, never a produced payload.
const declinedWords: ToolCallOf<'switch_mode'> = { id: 'x', name: 'ExitPlanMode', kind: 'switch_mode', request: { mode: 'plan' }, status: 'declined', images: [], result: proseResult('Not yet') }
void declinedWords
const declinedFailure: ToolCallOf<'read'> = { id: 'x', name: 'Read', kind: 'read', request: { path: '/a' }, status: 'declined', images: [], result: failedResult('refused') }
void declinedFailure
// @ts-expect-error Declined: a read that never ran produced no file body.
const declinedRead: ToolCallOf<'read'> = { id: 'x', name: 'Read', kind: 'read', request: { path: '/a' }, status: 'declined', images: [], result: readResult }
void declinedRead
// @ts-expect-error Declined: the unparsed brand states the call completed, which a refused one did not.
const declinedUnparsed: ToolCallOf<'read'> = { id: 'x', name: 'Read', kind: 'read', request: { path: '/a' }, status: 'declined', images: [], result: unparsedResult('raw') }
void declinedUnparsed

// Cancelled keeps whatever partial body it printed, in any of the three shapes.
const partialTyped: ToolCallOf<'read'> = { id: 'c', name: 'Read', kind: 'read', request: { path: '/a' }, status: 'cancelled', images: [], result: readResult }
const partialFailed: ToolCallOf<'read'> = { id: 'c', name: 'Read', kind: 'read', request: { path: '/a' }, status: 'cancelled', images: [], result: failedResult('cut') }
const partialUnparsed: ToolCallOf<'read'> = { id: 'c', name: 'Read', kind: 'read', request: { path: '/a' }, status: 'cancelled', images: [], result: unparsedResult('raw') }
void [partialTyped, partialFailed, partialUnparsed]

// I6: a generic kind's pictures ride in its result content, never on the call.
const bare: ToolCallOf<'mcp'> = { id: 'g', name: 'Tool', kind: 'mcp', request: { server: 's', tool: 't', args: {} }, status: 'completed', images: [], result: { content: [] } }
void bare
const pictured: ToolCallOf<'read'> = { id: 'g', name: 'Read', kind: 'read', request: { path: '/a' }, status: 'completed', images: [picture], result: readResult }
void pictured
// @ts-expect-error I6: a generic kind's pictures ride in its result content, never on the call.
const withPictures: ToolCallOf<'mcp'> = { id: 'g', name: 'Tool', kind: 'mcp', request: { server: 's', tool: 't', args: {} }, status: 'completed', images: [picture], result: { content: [] } }
void withPictures

// ---------------------------------------------------------------------------
// Distribution preserves correlation, compile-only.
//
// `ToolCallOf` and `ToolCallPayload` are distributive conditionals: a UNION of
// kinds must answer the UNION of each kind's correlated member -- never one
// object whose request is every kind's request at once. These expectations pin
// that, and pin the request/result correlation inside one member, so a refactor
// that loses the distribution fails `tsc` here rather than silently widening a
// renderer's input.
// ---------------------------------------------------------------------------

type Equal<A, B> = (<T>() => T extends A ? 1 : 2) extends (<T>() => T extends B ? 1 : 2) ? true : false
type Expect<T extends true> = T

/** The six expectations, joined so the linter reads each name as used. */
export type ToolCallCorrelationChecks = [
  Expect<Equal<ToolCallOf<'edit' | 'read'>, ToolCallOf<'edit'> | ToolCallOf<'read'>>>,
  Expect<Equal<ToolCallOf<ToolKind>, ToolCallIR>>,
  Expect<Equal<ToolCallPayload<'edit' | 'read'>, ToolCallPayload<'edit'> | ToolCallPayload<'read'>>>,
  Expect<Equal<ToolCallPayload<ToolKind>, ToolCallPayloadIR>>,
  Expect<Equal<ToolCallOf<'edit'>['request'], ToolRequests['edit']>>,
  Expect<Equal<ToolCallPayload<'read'>['result'], ToolResultOf<'read'> | undefined>>,
]

/** Named for the error message a violated check prints. */
export type ToolCallCorrelationCheckNames = keyof {
  callsDistribute: ToolCallCorrelationChecks[0]
  callsCoverTheUnion: ToolCallCorrelationChecks[1]
  payloadsDistribute: ToolCallCorrelationChecks[2]
  payloadsCoverTheUnion: ToolCallCorrelationChecks[3]
  oneMemberKeepsItsRequest: ToolCallCorrelationChecks[4]
  onePayloadKeepsItsResult: ToolCallCorrelationChecks[5]
}
