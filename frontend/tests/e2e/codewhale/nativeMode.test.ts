import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { mkdirSync, mkdtempSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { hasCodewhaleOperateContract, readCodewhaleNativeMode } from './nativeMode'

const scratch = resolve(process.cwd(), '../.tmp')
let directory: string
let home: string
let workingDir: string
const threadId = 'thr_native_mode'

beforeEach(() => {
  mkdirSync(scratch, { recursive: true })
  directory = mkdtempSync(join(scratch, 'codewhale-native-mode-'))
  home = join(directory, 'home')
  workingDir = join(directory, 'work')
  mkdirSync(home)
  mkdirSync(workingDir)
})
afterEach(() => rmSync(directory, { recursive: true, force: true }))

function writeRecord(record: unknown, store = 'store-native'): string {
  const path = join(home, 'leapmux', store, 'runtime', 'threads', `${threadId}.json`)
  mkdirSync(dirname(path), { recursive: true })
  writeFileSync(path, JSON.stringify(record))
  return path
}

function read() {
  return readCodewhaleNativeMode({
    home,
    runDir: directory,
    threadId,
    workingDir,
  })
}

describe('readCodewhaleNativeMode', () => {
  it.each(['agent', 'plan', 'operate'] as const)('reads the native %s state and keeps its original record', (mode) => {
    const record = {
      id: threadId,
      workspace: workingDir,
      mode,
      permission_posture: 'ask',
    }
    const path = writeRecord(record)
    expect(read()).toEqual({ mode, path, record })
  })

  it('refuses a missing or ambiguous native thread', () => {
    expect(read).toThrow('no private store')
    writeRecord({ id: threadId, workspace: workingDir, mode: 'agent' })
    writeRecord({ id: threadId, workspace: workingDir, mode: 'operate' }, 'store-other')
    expect(read).toThrow('exactly one')
  })

  it.each([
    { record: null, reason: 'another owner' },
    { record: {}, reason: 'another owner' },
    {
      record: { id: 'foreign', mode: 'operate' },
      reason: 'another owner',
    },
    {
      record: { id: threadId, workspace: '', mode: 'operate' },
      reason: 'must be nonempty',
    },
    {
      record: { id: threadId, workspace: 0, mode: 'operate' },
      reason: 'another owner',
    },
  ])('refuses a malformed or foreign owner with $reason: $record', ({ record, reason }) => {
    writeRecord(record)
    expect(read).toThrow(reason)
  })

  it.each(['', 'unknown', 0, -1, null])('refuses an invalid mode: %j', (mode) => {
    writeRecord({ id: threadId, workspace: workingDir, mode })
    expect(read).toThrow('unknown mode')
  })

  it('refuses a different workspace and a file that is too large', () => {
    const other = join(directory, 'other')
    mkdirSync(other)
    writeRecord({ id: threadId, workspace: other, mode: 'operate' })
    expect(read).toThrow('another workspace')
    const path = writeRecord({ id: threadId, workspace: workingDir, mode: 'operate' })
    writeFileSync(path, 'x'.repeat(1024 * 1024 + 1))
    expect(read).toThrow('within one mebibyte')
  })

  it('refuses a record that resolves outside its private run', () => {
    const outside = join(directory, '..', `${threadId}-${directory.split(/[\\/]/u).at(-1)}.json`)
    try {
      writeFileSync(outside, JSON.stringify({ id: threadId, workspace: workingDir, mode: 'operate' }))
      const target = join(home, 'leapmux', 'store-native', 'runtime', 'threads', `${threadId}.json`)
      mkdirSync(dirname(target), { recursive: true })
      symlinkSync(outside, target)
      expect(read).toThrow('outside')
    }
    finally {
      rmSync(outside, { force: true })
    }
  })

  it.each([
    '',
    '.',
    '..',
    '../other',
    'other/session',
    'other\\session',
    'zero\0byte',
  ])('refuses an unsafe thread ID: %j', (threadId) => {
    expect(() => readCodewhaleNativeMode({
      home,
      runDir: directory,
      threadId,
      workingDir,
    })).toThrow('one thread ID')
  })
})

const contract = '<codewhale:runtime_event kind="operate_contract" visibility="internal">\nOrchestrate by default: run a `workflow` and parallel agent workers.\n</codewhale:runtime_event>\n<turn_meta>\nInput provenance: runtime (non-authoritative)\n</turn_meta>'

function request(messages: unknown): MockModelRequestRecord {
  return {
    protocol: 'openai-chat-completions',
    path: '/v1/chat/completions',
    body: { messages },
  }
}

describe('hasCodewhaleOperateContract', () => {
  it('reads the complete native internal contract in a user-role message', () => {
    expect(hasCodewhaleOperateContract(request([
      { role: 'user', content: contract },
    ]))).toBe(true)
  })

  it.each([
    'system',
    'developer',
    'assistant',
    'tool',
  ])('refuses the %s role', (role) => {
    expect(hasCodewhaleOperateContract(request([
      { role, content: contract },
    ]))).toBe(false)
  })

  it.each([
    contract.slice(0, -1),
    `Quote this: ${contract}`,
    'Operate',
    '',
    null,
    false,
    0,
  ])('refuses a quoted or malformed contract: %j', (content) => {
    expect(hasCodewhaleOperateContract(request([
      { role: 'user', content },
    ]))).toBe(false)
  })

  it('refuses a malformed message collection', () => {
    expect(hasCodewhaleOperateContract(request(null))).toBe(false)
    expect(hasCodewhaleOperateContract(request({}))).toBe(false)
  })

  it('refuses a bare event without the native runtime provenance', () => {
    const bare = contract.slice(0, contract.indexOf('\n<turn_meta>'))
    expect(hasCodewhaleOperateContract(request([
      { role: 'user', content: bare },
    ]))).toBe(false)
  })
})
