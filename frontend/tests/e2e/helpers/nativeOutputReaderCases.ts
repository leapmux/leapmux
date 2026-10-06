/**
 * The cases that every provider's output path reader runs, from one place, and the Worker row builder for reader
 * fixtures.
 *
 * Ten copies of the same six cases were ten chances to leave a case out, and each copy accepted any thrown error.
 * A fixture typo then passed as a refusal. Each case here requires the error that its refusal states.
 *
 * This module imports vitest, so only a `*.test.ts` imports it. A `*.spec.ts` must never import it: Playwright runs
 * a spec without the vitest API.
 */
import type { MessageCompletion } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { NativeMessageSnapshot } from './nativeMessages'
import type { NativeOutputReceipt } from './nativeToolOutputFilePaths'
import { expect, it } from 'vitest'
import { makeMessage, rawContent } from '../../../src/test-support/messageFactory'

/** The native session of a reader fixture. */
export const READER_SESSION_ID = 'native-session'

/** The call of the native result that a reader fixture holds. */
export const READER_CALL_ID = 'native-call'

/** The text that the preview of a reader fixture holds. */
export const READER_PREVIEW = 'native preview'

/** The error of the shared record reader (`readNativeToolOutputRecord`) for no record or for two records. */
export const EXACTLY_ONE_RECORD = 'exactly one accepted record'

/** Pointers that are not a filesystem path: two URIs, blank text, and a path with a NUL. */
export const INVALID_POINTERS: readonly string[] = ['https://example.com/output', 'file:///native/output', ' ', '/native/zero\0byte']

/** One stored Worker row of a reader fixture. */
export interface NativeOutputRow {
  /** The native frame. The row stores it as JSON. */
  frame: unknown
  /** The span of the row. `READER_CALL_ID` by default. */
  spanId?: string
  spanType?: string
  /** The native session of the row. The session of the snapshot by default. */
  agentSessionId?: string
  completion?: MessageCompletion
  /** The whole stored supplement, as `nativeMessageSupplement` reads it. The row stores no supplement without it. */
  supplement?: unknown
}

/** Build the Worker snapshot of one agent whose stored rows hold `rows`, in order. */
export function nativeOutputSnapshot(
  rows: readonly NativeOutputRow[],
  owner: { agentId?: string, agentSessionId?: string } = {},
): NativeMessageSnapshot {
  const agentSessionId = owner.agentSessionId ?? READER_SESSION_ID
  return {
    agentId: owner.agentId ?? 'agent',
    agentSessionId,
    messages: rows.map((row, index) => makeMessage({
      id: `native-row-${index}`,
      seq: BigInt(index + 1),
      agentSessionId: row.agentSessionId ?? agentSessionId,
      spanId: row.spanId ?? READER_CALL_ID,
      ...(row.spanType === undefined ? {} : { spanType: row.spanType }),
      ...(row.completion === undefined ? {} : { completion: row.completion }),
      content: rawContent(row.frame),
      ...(row.supplement === undefined ? {} : { supplementalContent: rawContent(row.supplement) }),
    })),
  }
}

/** The facts of one provider reader that the contract cases need. */
export interface NativeOutputReaderContract {
  /** The reader under test. */
  read: (snapshot: NativeMessageSnapshot, callId: string) => NativeOutputReceipt
  /** A frame that `read` accepts for `READER_CALL_ID` in `READER_SESSION_ID`. Its preview holds `READER_PREVIEW`. */
  frame: Record<string, unknown>
  /** The one path that `frame` declares. */
  path: string
  /** The span of the stored row. `READER_CALL_ID` by default. */
  spanId?: string
  /** The error of the reader for a pointer that is not a filesystem path. */
  pointerError: string | RegExp
}

function contractSnapshot(contract: NativeOutputReaderContract, frame: unknown = contract.frame, supplement?: unknown): NativeMessageSnapshot {
  return nativeOutputSnapshot([{ frame, ...(contract.spanId === undefined ? {} : { spanId: contract.spanId }), supplement }])
}

/** Replace each JSON spelling of `from` in `frame` with the JSON spelling of `to`. */
function replacedFrame(frame: unknown, from: string, to: string): unknown {
  return JSON.parse(JSON.stringify(frame).replaceAll(JSON.stringify(from).slice(1, -1), JSON.stringify(to).slice(1, -1)))
}

/** Require the declared path, the preview, the original frame, and the original bytes of the stored row. */
export function expectReadsOriginalRecord(contract: NativeOutputReaderContract): void {
  const snapshot = contractSnapshot(contract)
  const receipt = contract.read(snapshot, READER_CALL_ID)
  expect(receipt.paths).toEqual([contract.path])
  expect(receipt.previewText).toContain(READER_PREVIEW)
  expect(receipt.frame).toEqual(contract.frame)
  expect(receipt.content).toEqual(snapshot.messages[0]?.content)
}

/** Require a refusal of a frame of another call in the span of the original call. */
export function expectRefusesForeignCall(contract: NativeOutputReaderContract): void {
  const foreign = replacedFrame(contract.frame, READER_CALL_ID, 'foreign-call')
  expect(() => contract.read(contractSnapshot(contract, foreign), READER_CALL_ID)).toThrow(EXACTLY_ONE_RECORD)
}

/** Require a refusal of a row of another native session, or of no session. */
export function expectRefusesForeignSession(contract: NativeOutputReaderContract, agentSessionId: string): void {
  const snapshot = contractSnapshot(contract)
  const row = snapshot.messages[0]
  if (!row)
    throw new Error('The reader fixture requires its original row.')
  row.agentSessionId = agentSessionId
  expect(() => contract.read(snapshot, READER_CALL_ID)).toThrow(EXACTLY_ONE_RECORD)
}

/** Require a refusal of two rows of the same result. */
export function expectRefusesDuplicateRecord(contract: NativeOutputReaderContract): void {
  const snapshot = contractSnapshot(contract)
  const row = snapshot.messages[0]
  if (!row)
    throw new Error('The reader fixture requires its original row.')
  snapshot.messages.push(row)
  expect(() => contract.read(snapshot, READER_CALL_ID)).toThrow(EXACTLY_ONE_RECORD)
}

/** Require the reader's own refusal of a pointer that is not a filesystem path. */
export function expectRefusesPointer(contract: NativeOutputReaderContract, pointer: string): void {
  const frame = replacedFrame(contract.frame, contract.path, pointer)
  expect(() => contract.read(contractSnapshot(contract, frame), READER_CALL_ID)).toThrow(contract.pointerError)
}

/** Require the native preview, not the text of a supplement that the Worker no longer writes. */
export function expectIgnoresForgedSupplement(contract: NativeOutputReaderContract): void {
  const supplement = { provider: { outputFile: { path: contract.path, text: 'FORGED_FILE_BODY' } } }
  expect(contract.read(contractSnapshot(contract, contract.frame, supplement), READER_CALL_ID).previewText).not.toContain('FORGED_FILE_BODY')
}

/**
 * Run the six contract cases of one output path reader.
 * Call it inside the provider's own `describe` for the reader, as `describe('readKiroNativeOutput', ...)`.
 */
export function runNativeOutputReaderCases(contract: NativeOutputReaderContract): void {
  it('reads the exact native path and preserves the original packet bytes', () => {
    expectReadsOriginalRecord(contract)
  })

  it('refuses a different call while the original Worker span stays fixed', () => {
    expectRefusesForeignCall(contract)
  })

  it.each(['', 'foreign-session'])('refuses a missing or foreign Worker session: %j', (agentSessionId) => {
    expectRefusesForeignSession(contract, agentSessionId)
  })

  it('refuses duplicate native result packets', () => {
    expectRefusesDuplicateRecord(contract)
  })

  it.each(INVALID_POINTERS)('refuses a non-filesystem pointer: %j', (pointer) => {
    expectRefusesPointer(contract, pointer)
  })

  it('preserves the native preview when a discarded feature supplement supplies other text', () => {
    expectIgnoresForgedSupplement(contract)
  })
}
