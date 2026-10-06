import type { NativeMessageSnapshot } from './nativeMessages'
import type { NativeOutputReaderContract } from './nativeOutputReaderCases'
import type { NativeOutputReceipt } from './nativeToolOutputFilePaths'
import { describe, expect, it } from 'vitest'
import { MessageCompletion } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isFilesystemPath } from '../../../src/lib/paths'
import { nativeMessageBody, nativeMessageSupplement, readNativeToolOutputRecord } from './nativeMessages'
import {
  EXACTLY_ONE_RECORD,
  expectIgnoresForgedSupplement,
  expectReadsOriginalRecord,
  expectRefusesDuplicateRecord,
  expectRefusesForeignCall,
  expectRefusesForeignSession,
  expectRefusesPointer,
  INVALID_POINTERS,
  nativeOutputSnapshot,
  READER_CALL_ID,
  READER_PREVIEW,
  READER_SESSION_ID,
  runNativeOutputReaderCases,
} from './nativeOutputReaderCases'

const frame = { toolCallId: READER_CALL_ID, output: READER_PREVIEW, outputPath: '/native/sample/output.txt' }
const POINTER_ERROR = 'The sample result has no filesystem pointer.'

/** A reader that keeps the contract: one record through the shared reader, and its own pointer refusal. */
function sampleReader(snapshot: NativeMessageSnapshot, callId: string): NativeOutputReceipt {
  const record = readNativeToolOutputRecord(snapshot, { callId, spanId: callId, accepts: value => value.toolCallId === callId })
  const path = record.frame.outputPath
  if (!isFilesystemPath(path))
    throw new Error(POINTER_ERROR)
  return { paths: [path], previewText: String(record.frame.output), frame: record.frame, content: record.message.content }
}

const contract: NativeOutputReaderContract = { read: sampleReader, frame, path: frame.outputPath, pointerError: POINTER_ERROR }

/** A reader that takes the first row of the span: it checks no call, no session, and no duplicate. */
function firstRowReader(snapshot: NativeMessageSnapshot, callId: string): NativeOutputReceipt {
  const message = snapshot.messages.find(row => row.spanId === callId)
  if (!message)
    throw new Error('The careless reader found no row.')
  const value = nativeMessageBody(message) as typeof frame
  return { paths: [value.outputPath], previewText: value.output, frame: value, content: message.content }
}

describe('nativeOutputSnapshot', () => {
  it('stores each frame as JSON in its own row of the reader session and call span', () => {
    const snapshot = nativeOutputSnapshot([{ frame }, { frame: { second: true } }])
    expect(snapshot.agentSessionId).toBe(READER_SESSION_ID)
    expect(snapshot.messages.map(row => [row.id, row.seq, row.agentSessionId, row.spanId])).toEqual([
      ['native-row-0', 1n, READER_SESSION_ID, READER_CALL_ID],
      ['native-row-1', 2n, READER_SESSION_ID, READER_CALL_ID],
    ])
    expect(snapshot.messages.map(nativeMessageBody)).toEqual([frame, { second: true }])
    expect(snapshot.messages.map(nativeMessageSupplement)).toEqual([undefined, undefined])
  })

  it('keeps the span, span type, session, completion, and supplement of a row', () => {
    const supplement = { provider: { toolCallId: READER_CALL_ID } }
    const snapshot = nativeOutputSnapshot(
      [{ frame, spanId: 'span', spanType: 'execute', agentSessionId: 'row-session', completion: MessageCompletion.COMPLETE, supplement }],
      { agentId: 'owner', agentSessionId: 'owner-session' },
    )
    const row = snapshot.messages[0]
    expect([snapshot.agentId, snapshot.agentSessionId]).toEqual(['owner', 'owner-session'])
    expect([row?.spanId, row?.spanType, row?.agentSessionId, row?.completion]).toEqual(['span', 'execute', 'row-session', MessageCompletion.COMPLETE])
    expect(row && nativeMessageSupplement(row)).toEqual(supplement)
  })

  it('builds an empty snapshot for no row', () => {
    expect(nativeOutputSnapshot([]).messages).toEqual([])
  })
})

describe('runNativeOutputReaderCases', () => {
  // The emitter itself, against a reader that keeps the contract.
  runNativeOutputReaderCases(contract)

  it('states the refusal of the shared record reader', () => {
    expect(() => sampleReader(nativeOutputSnapshot([]), READER_CALL_ID)).toThrow(EXACTLY_ONE_RECORD)
  })

  it('fails a reader that accepts a frame of another call', () => {
    expect(() => expectRefusesForeignCall({ ...contract, read: firstRowReader })).toThrow('to throw an error')
  })

  it.each(['', 'foreign-session'])('fails a reader that accepts a row of the session %j', (agentSessionId) => {
    expect(() => expectRefusesForeignSession({ ...contract, read: firstRowReader }, agentSessionId)).toThrow('to throw an error')
  })

  it('fails a reader that takes the first of two rows', () => {
    expect(() => expectRefusesDuplicateRecord({ ...contract, read: firstRowReader })).toThrow('to throw an error')
  })

  it.each(INVALID_POINTERS)('fails a reader that accepts the pointer %j', (pointer) => {
    expect(() => expectRefusesPointer({ ...contract, read: firstRowReader }, pointer)).toThrow('to throw an error')
  })

  it('fails a reader that refuses a pointer with another error', () => {
    expect(() => expectRefusesPointer({ ...contract, pointerError: 'another pointer error' }, INVALID_POINTERS[0]!)).toThrow('another pointer error')
  })

  it('fails a reader that takes its preview from the supplement', () => {
    const supplementReader = (snapshot: NativeMessageSnapshot, callId: string): NativeOutputReceipt => {
      const receipt = sampleReader(snapshot, callId)
      const message = snapshot.messages[0]
      return { ...receipt, previewText: JSON.stringify(message && nativeMessageSupplement(message)) }
    }
    expect(() => expectIgnoresForgedSupplement({ ...contract, read: supplementReader })).toThrow('FORGED_FILE_BODY')
  })

  it('fails a reader that returns other bytes than the stored row', () => {
    const copyReader = (snapshot: NativeMessageSnapshot, callId: string): NativeOutputReceipt => ({ ...sampleReader(snapshot, callId), content: new Uint8Array([1]) })
    expect(() => expectReadsOriginalRecord({ ...contract, read: copyReader })).toThrow('to deeply equal')
  })
})
