import type { KiroGoalSessionIdentity } from './goalReceipt'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { decodeKiroGoalMessages, kiroGoalCancellation, kiroGoalExecutionId, kiroGoalSessionId, readKiroGoalMessages } from './goalReceipt'

const EXECUTION = '8dfebe85-fb8d-4af6-ac95-ffbf7886d960'
const SESSION = 'sess_115a7292-18d2-4aca-bc4d-4048e8715bf9'
const PARENT = 'sess_841a1b40-6cca-4177-89bd-91bc526d48c6'
const START = { id: `${EXECUTION}-turn-start`, timestamp: '2026-10-01T09:50:21.420Z', payload: { type: 'turn_start', executionId: EXECUTION } }
const USAGE = { id: `${EXECUTION}-usage`, timestamp: '2026-10-01T09:50:21.472Z', payload: { type: 'usage_summary', promptTurnSummaries: [], elapsedTime: 52, status: 'aborted', executionId: EXECUTION } }
const END = { id: `${EXECUTION}-turn-end`, timestamp: '2026-10-01T09:50:21.472Z', payload: { type: 'turn_end', stopReason: 'cancelled', executionId: EXECUTION } }
const encoded = (records: readonly unknown[]) => `${records.map(record => JSON.stringify(record)).join('\n')}\n`

describe('kiroGoalSessionId', () => {
  it('uses the actual native conversation ID rather than a guessed workflow or child ID', () => {
    expect(kiroGoalSessionId({ protocol: 'aws-event-stream', path: '/', body: { conversationState: { conversationId: SESSION } } })).toBe(SESSION)
  })

  it.each([null, [], {}, { conversationState: { conversationId: '' } }, { conversationState: { conversationId: 0 } }].map(body => ({ body })))('rejects an absent or malformed native session: %j', ({ body }) => {
    expect(() => kiroGoalSessionId({ protocol: 'aws-event-stream', path: '/', body })).toThrow('actual native session ID')
  })

  it('rejects another model protocol even when its body contains the same field', () => {
    expect(() => kiroGoalSessionId({ protocol: 'openai-chat-completions', path: '/', body: { conversationState: { conversationId: SESSION } } })).toThrow('actual native session ID')
  })
})

describe('decodeKiroGoalMessages', () => {
  it('retains the captured native start, aborted usage, and cancelled end', () => {
    const messages = decodeKiroGoalMessages(encoded([START, USAGE, END]))
    expect(kiroGoalExecutionId(messages)).toBe(EXECUTION)
    expect(kiroGoalCancellation(messages, EXECUTION)).toEqual({ executionId: EXECUTION, startMessageId: START.id, usageMessageId: USAGE.id, endMessageId: END.id })
  })

  it('waits for an incomplete append tail and rejects a malformed completed line', () => {
    const text = encoded([START, USAGE])
    expect(kiroGoalCancellation(decodeKiroGoalMessages(`${text}{"id":"unfinished`), EXECUTION)).toBeUndefined()
    expect(kiroGoalCancellation(decodeKiroGoalMessages(`${text}${JSON.stringify(END)}`), EXECUTION)).toBeUndefined()
    expect(kiroGoalCancellation(decodeKiroGoalMessages(`${text}${JSON.stringify(END)}\n`), EXECUTION)?.executionId).toBe(EXECUTION)
    expect(() => decodeKiroGoalMessages(`${text}{broken}\n`)).toThrow('malformed complete JSON line')
  })

  it('retains an empty pending file without creating a cancellation receipt', () => {
    expect(decodeKiroGoalMessages('')).toEqual([])
    expect(kiroGoalExecutionId([])).toBeUndefined()
    expect(kiroGoalCancellation([], EXECUTION)).toBeUndefined()
  })

  it.each([null, [], {}, { id: '', payload: {} }, { id: 'entry', payload: null }].map(record => ({ record })))('rejects an invalid completed native record: %j', ({ record }) => {
    expect(() => decodeKiroGoalMessages(encoded([record]))).toThrow('invalid message record')
  })

  it.each([[USAGE, END], [START, END], [START, USAGE]].map(records => ({ records })))('waits for a missing cancellation record: %j', ({ records }) => {
    expect(kiroGoalCancellation(decodeKiroGoalMessages(encoded(records)), EXECUTION)).toBeUndefined()
  })

  it.each([[END, USAGE, START], [START, END, USAGE]].map(records => ({ records })))('refuses complete cancellation records in the wrong order: %j', ({ records }) => {
    expect(() => kiroGoalCancellation(decodeKiroGoalMessages(encoded(records)), EXECUTION)).toThrow('The native Kiro cancellation receipts are out of order.')
  })

  it('refuses an old execution, mismatched fields, and a successful or ordinary turn end', () => {
    const messages = decodeKiroGoalMessages(encoded([START, USAGE, END]))
    expect(kiroGoalCancellation(messages, 'the-resumed-execution')).toBeUndefined()
    for (const altered of [
      { ...USAGE, payload: { ...USAGE.payload, executionId: 'another-execution' } },
      { ...USAGE, payload: { ...USAGE.payload, status: 'success' } },
      { ...USAGE, id: 'wrong-receipt-id' },
    ]) {
      expect(kiroGoalCancellation(decodeKiroGoalMessages(encoded([START, altered, END])), EXECUTION)).toBeUndefined()
    }
    expect(kiroGoalCancellation(decodeKiroGoalMessages(encoded([START, USAGE, { ...END, payload: { ...END.payload, stopReason: 'end_turn' } }])), EXECUTION)).toBeUndefined()
  })

  it('refuses duplicate receipts and an empty requested execution ID', () => {
    expect(() => kiroGoalCancellation(decodeKiroGoalMessages(encoded([START, USAGE, USAGE, END])), EXECUTION)).toThrow('repeats one execution receipt')
    expect(() => kiroGoalCancellation([], '')).toThrow('captured execution ID')
  })

  it('selects the actual latest execution without reusing an earlier cancellation', () => {
    const next = { id: 'actual-resumed-execution-turn-start', payload: { type: 'turn_start', executionId: 'actual-resumed-execution' } }
    const messages = decodeKiroGoalMessages(encoded([START, USAGE, END, next]))
    expect(kiroGoalExecutionId(messages)).toBe('actual-resumed-execution')
    expect(kiroGoalCancellation(messages, 'actual-resumed-execution')).toBeUndefined()
    expect(() => kiroGoalExecutionId([{ id: 'wrong', payload: START.payload }])).toThrow('exact execution ID')
  })
})

describe('readKiroGoalMessages', () => {
  let directory: string
  let identity: KiroGoalSessionIdentity
  let sessionDirectory: string
  beforeEach(() => {
    const scratch = resolve(process.cwd(), '../.tmp')
    mkdirSync(scratch, { recursive: true })
    directory = mkdtempSync(join(scratch, 'kiro-goal-receipt-unit-'))
    identity = { home: join(directory, 'home'), workingDir: join(directory, 'workspace'), runDir: directory, sessionId: SESSION, parentSessionId: PARENT }
    mkdirSync(identity.home)
    mkdirSync(identity.workingDir)
    sessionDirectory = join(identity.home, '.kiro', 'sessions', 'actual-native-bucket', SESSION)
  })
  afterEach(() => rmSync(directory, { recursive: true, force: true }))
  const writeSession = (path: string, identity: KiroGoalSessionIdentity) => {
    mkdirSync(path, { recursive: true })
    writeFileSync(join(path, 'session.json'), JSON.stringify({ id: identity.sessionId, workspacePaths: [identity.workingDir], rootConversationId: identity.parentSessionId }))
    writeFileSync(join(path, 'messages.jsonl'), encoded([START, USAGE, END]))
  }

  it('reads the actual private child bytes with exact workspace and parent metadata', () => {
    writeSession(sessionDirectory, identity)
    expect(kiroGoalCancellation(readKiroGoalMessages(identity) ?? [], EXECUTION)?.executionId).toBe(EXECUTION)
    expect(readFileSync(join(sessionDirectory, 'messages.jsonl'), 'utf8')).toBe(encoded([START, USAGE, END]))
  })

  it('returns pending for an absent store or an unwritten child session', () => {
    expect(readKiroGoalMessages(identity)).toBeNull()
    mkdirSync(dirname(sessionDirectory), { recursive: true })
    expect(readKiroGoalMessages(identity)).toBeNull()
  })

  it.each(['', '.', '..', '../another-session', 'nested/session', 'nested\\session', 'bad\0session'])('rejects an invalid session component before a native read: %j', (sessionId) => {
    expect(() => readKiroGoalMessages({ ...identity, sessionId })).toThrow('one filename component')
  })

  it('refuses another native session or parent workspace instead of accepting its cancellation', () => {
    writeSession(sessionDirectory, identity)
    for (const metadata of [
      { id: 'wrong-session', workspacePaths: [identity.workingDir], rootConversationId: PARENT },
      { id: SESSION, workspacePaths: [identity.workingDir], rootConversationId: 'wrong-parent' },
      { id: SESSION, workspacePaths: [identity.home], rootConversationId: PARENT },
      { id: SESSION, workspacePaths: [0], rootConversationId: PARENT },
    ]) {
      writeFileSync(join(sessionDirectory, 'session.json'), JSON.stringify(metadata))
      expect(() => readKiroGoalMessages(identity)).toThrow('exact child workspace and parent session')
    }
  })

  it('refuses duplicate native session locations', () => {
    writeSession(sessionDirectory, identity)
    writeSession(join(identity.home, '.kiro', 'sessions', 'another-native-bucket', SESSION), identity)
    expect(() => readKiroGoalMessages(identity)).toThrow('multiple files')
  })

  it('refuses a messages symlink outside the private HOME before reading its bytes', () => {
    writeSession(sessionDirectory, identity)
    const external = join(directory, 'external-messages.jsonl')
    writeFileSync(external, encoded([START, USAGE, END]))
    const file = join(sessionDirectory, 'messages.jsonl')
    rmSync(file)
    symlinkSync(external, file)
    expect(() => readKiroGoalMessages(identity)).toThrow('resolves outside')
  })

  it('refuses a HOME outside the run and preserves the actual native metadata read failure', () => {
    expect(() => readKiroGoalMessages({ ...identity, home: dirname(directory) })).toThrow('resolves outside')
    writeSession(sessionDirectory, identity)
    writeFileSync(join(sessionDirectory, 'session.json'), '{broken}')
    expect(() => readKiroGoalMessages(identity)).toThrow(SyntaxError)
  })
})
