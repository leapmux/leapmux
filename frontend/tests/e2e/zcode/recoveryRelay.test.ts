import type { RecoveryDirection } from './recoveryRelay'
import { Buffer } from 'node:buffer'
import { spawn } from 'node:child_process'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { stopProcess } from '../helpers/process'
import { createZcodeRecoveryTransformer, writeZcodeRecoveryRelay } from './recoveryRelay'

function fixture() {
  const relay = createZcodeRecoveryTransformer('selected-prompt')
  const frame = (direction: RecoveryDirection, value: unknown) => relay.transform(direction, `${JSON.stringify(value)}\n`)
  const worker = (value: unknown) => frame('worker', value)
  const native = (value: unknown) => frame('native', value)
  worker({
    id: 0,
    method: 'session/send',
    params: { sessionId: 's', inputId: 'i', content: 'selected-prompt' },
  })
  native({
    method: 'session/event',
    params: {
      sessionId: 's',
      turnId: 't',
      seq: 2,
      type: 'turn.started',
      payload: { inputId: 'i' },
    },
  })
  return { relay, worker, native }
}
function end(seq = 5, sessionId = 's', turnId = 't') {
  return {
    sessionId,
    turnId,
    eventId: 'native-end',
    seq,
    type: 'turn.completed',
    payload: { resultType: 'success' },
  }
}

describe('createZcodeRecoveryTransformer', () => {
  it('omits only the selected end and preserves repeated omission', () => {
    const { native, relay } = fixture()
    expect(native({
      method: 'session/event',
      params: end(),
    })).toBeNull()
    expect(relay.selected()).toEqual({
      sessionId: 's',
      inputId: 'i',
      turnId: 't',
      omittedSeq: 5,
    })
    expect(native({
      method: 'session/event',
      params: end(),
    })).toBeNull()
    expect(JSON.parse(native({
      method: 'session/event',
      params: end(6, 's', 'next'),
    })!)).toMatchObject({
      params: { seq: 5, turnId: 'next' },
    })
    expect(JSON.parse(native({
      method: 'session/event',
      params: end(8, 'other', 't'),
    })!)).toMatchObject({
      params: { sessionId: 'other', seq: 8 },
    })
  })

  it.each([
    [3, 3],
    [4, 5],
    [5, 6],
  ])('translates visible cursor %i to native %i', (visible, expected) => {
    const { worker, native } = fixture()
    native({
      method: 'session/event',
      params: end(),
    })
    for (const method of ['session/events', 'session/subscribe']) {
      expect(JSON.parse(worker({
        id: method,
        method,
        params: { sessionId: 's', afterSeq: visible },
      })!)).toMatchObject({
        params: { afterSeq: expected },
      })
    }
  })

  it.each(['session/events', 'session/subscribe'])('discovers an omitted end in %s before projecting watermarks', (method) => {
    const { worker, native } = fixture()
    worker({
      id: 0,
      method,
      params: { sessionId: 's', afterSeq: 2 },
    })
    expect(JSON.parse(native({
      id: 0,
      result: {
        sessionId: 's',
        eventSeq: 6,
        events: [
          { seq: 4, type: 'part.upserted' },
          end(),
          { seq: 6, type: 'session.updated' },
        ],
        snapshot: {
          session: { sessionId: 's' },
          runtime: { eventSeq: 6, activeTurnId: '' },
        },
      },
    })!).result).toEqual({
      sessionId: 's',
      eventSeq: 5,
      events: [
        { seq: 4, type: 'part.upserted' },
        { seq: 5, type: 'session.updated' },
      ],
      snapshot: {
        session: { sessionId: 's' },
        runtime: { eventSeq: 5, activeTurnId: '' },
      },
    })
  })

  it('discovers an out-of-order start before selecting an end from the same replay batch', () => {
    const relay = createZcodeRecoveryTransformer('marker')
    relay.transform('worker', '{"id":1,"method":"session/send","params":{"sessionId":"s","inputId":"i","content":"marker"}}\n')
    relay.transform('worker', '{"id":2,"method":"session/events","params":{"sessionId":"s"}}\n')
    const projected = relay.transform('native', JSON.stringify({
      id: 2,
      result: {
        eventSeq: 5,
        events: [
          end(),
          {
            sessionId: 's',
            turnId: 't',
            seq: 2,
            type: 'turn.started',
            payload: { inputId: 'i' },
          },
        ],
      },
    }))
    expect(JSON.parse(projected!).result).toEqual({
      eventSeq: 4,
      events: [
        {
          sessionId: 's',
          turnId: 't',
          seq: 2,
          type: 'turn.started',
          payload: { inputId: 'i' },
        },
      ],
    })
  })

  it('preserves boundary and message bytes after omission is active', () => {
    const { relay, worker, native } = fixture()
    native({
      method: 'session/event',
      params: end(),
    })
    const boundary = '{"method":"state.updated","params":{"scope":"session","sessionId":"s","revision":8,"reason":"prompt_completed","patch":{"projection":{"currentTurnId":"t"}}}}\r\n'
    expect(relay.transform('native', boundary)).toBe(boundary)
    worker({
      id: 13,
      method: 'session/messages',
      params: { sessionId: 's' },
    })
    const messages = '{"id":13,"result":{"messages":[{"seq":10,"text":"생각 🧪"}]}}\r\n'
    expect(relay.transform('native', messages)).toBe(messages)
    expect(relay.transform('native', 'not JSON\n')).toBe('not JSON\n')
  })

  it('correlates numeric and string response IDs without consuming native requests', () => {
    const { worker, native } = fixture()
    native({
      method: 'session/event',
      params: end(),
    })
    worker({
      id: 0,
      method: 'session/read',
      params: { sessionId: 's' },
    })
    worker({
      id: '0',
      method: 'session/read',
      params: { sessionId: 'other' },
    })
    const serverRequest = {
      id: 0,
      method: 'interaction/requestPermission',
      params: { requestId: 'permission' },
    }
    expect(JSON.parse(native(serverRequest)!)).toEqual(serverRequest)
    expect(JSON.parse(native({
      id: '0',
      result: {
        runtime: { eventSeq: 9 },
      },
    })!)).toMatchObject({
      result: {
        runtime: { eventSeq: 9 },
      },
    })
    expect(JSON.parse(native({
      id: 0,
      result: {
        runtime: { eventSeq: 9 },
      },
    })!)).toMatchObject({
      result: {
        runtime: { eventSeq: 8 },
      },
    })
  })

  it('uses a nested event outer session rather than selecting another session', () => {
    const { native, relay } = fixture()
    const foreign = {
      method: 'session/event',
      params: {
        sessionId: 'other',
        event: { turnId: 't', seq: 5, type: 'turn.completed' },
      },
    }
    expect(JSON.parse(native(foreign)!)).toEqual(foreign)
    expect(relay.selected()?.omittedSeq).toBeUndefined()
  })

  it('ignores a background start and a different input', () => {
    const relay = createZcodeRecoveryTransformer('marker')
    relay.transform('worker', '{"id":1,"method":"session/send","params":{"sessionId":"s","inputId":"i","content":"marker"}}')
    for (const payload of [
      { inputId: 'i', inputSource: 'background_task' },
      { inputId: 'other' },
    ]) {
      relay.transform('native', JSON.stringify({
        method: 'session/event',
        params: {
          sessionId: 's',
          turnId: 'background',
          seq: 1,
          type: 'turn.started',
          payload,
        },
      }))
    }
    expect(relay.selected()).toEqual({ sessionId: 's', inputId: 'i' })
  })

  it.each([
    -1,
    0,
    Number.NaN,
    Number.MAX_SAFE_INTEGER + 1,
  ])('refuses an invalid selected end sequence %s', (seq) => {
    const { native } = fixture()
    expect(() => native({
      method: 'session/event',
      params: end(seq),
    })).toThrow('sequence')
  })
})

let directory: string
const children: ReturnType<typeof spawn>[] = []
beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  directory = mkdtempSync(join(scratch, 'zcode-relay-unit-'))
})
afterEach(async () => {
  await Promise.all(children.splice(0).map(child => stopProcess(child)))
  rmSync(directory, { recursive: true, force: true })
})

async function runFixture(source: string, input: Buffer, argv = ['app-server', '--stdio']) {
  const native = join(directory, 'native child.cjs')
  writeFileSync(native, source)
  const evidencePath = join(directory, 'evidence.jsonl')
  const relay = writeZcodeRecoveryRelay(directory, {
    launch: {
      binaryName: 'zcode',
      executable: process.execPath,
      args: [native],
    },
    marker: 'marker',
    evidencePath,
  })
  const child = spawn(process.execPath, [relay.scriptPath, ...argv], {
    stdio: ['pipe', 'pipe', 'pipe'],
  })
  children.push(child)
  const output: Buffer[] = []
  const stderr: Buffer[] = []
  child.stdout.on('data', chunk => output.push(Buffer.from(chunk)))
  child.stderr.on('data', chunk => stderr.push(Buffer.from(chunk)))
  const result = new Promise<{ code: number | null, output: Buffer, stderr: string }>((resolveResult, reject) => {
    child.once('error', reject)
    child.once('close', code => resolveResult({
      code,
      output: Buffer.concat(output),
      stderr: Buffer.concat(stderr).toString(),
    }))
  })
  // Split every byte, including multibyte Unicode and incomplete final lines.
  for (const byte of input)
    child.stdin.write(Buffer.from([byte]))
  child.stdin.end()
  return {
    ...await result,
    evidencePath,
  }
}

describe('writeZcodeRecoveryRelay', () => {
  it('preserves split Unicode and final fragments in both directions', async () => {
    const input = Buffer.from(' {"text":"생각 🧪"}\r\nfragment 🧪')
    const result = await runFixture(`process.stdin.on('data', chunk => { for (const byte of chunk) process.stdout.write(Buffer.from([byte])); });`, input)
    expect(result.code, result.stderr).toBe(0)
    expect(result.output).toEqual(input)
    const evidence = readFileSync(result.evidencePath, 'utf8').trim().split('\n').map(line => JSON.parse(line) as { fragment: boolean })
    expect(evidence.filter(record => record.fragment)).toHaveLength(2)
  })

  it('preserves unmodified non-UTF-8 bytes in both directions', async () => {
    const input = Buffer.from([0xFF, 0x0A, 0xFE])
    const result = await runFixture(`process.stdin.on('data', chunk => process.stdout.write(chunk));`, input)
    expect(result.code, result.stderr).toBe(0)
    expect(result.output).toEqual(input)
    const evidence = readFileSync(result.evidencePath, 'utf8').trim().split('\n').map(line => JSON.parse(line) as { raw: string, forwarded: string })
    for (const record of evidence)
      expect(record.forwarded).toBe(record.raw)
  })

  it('passes storage preparation through and preserves the native exit code', async () => {
    const result = await runFixture('process.stdout.write(JSON.stringify(process.argv.slice(2))); process.exitCode = 7;', Buffer.alloc(0), ['app-server', '--stdio', '--prepare-storage'])
    expect(result.code).toBe(7)
    expect(JSON.parse(result.output.toString())).toEqual(['app-server', '--stdio', '--prepare-storage'])
    expect(readFileSync(result.evidencePath, 'utf8')).toBe('')
  })

  it('reports a failed native child', async () => {
    const result = await runFixture('process.stdin.resume(); process.stdin.on("end", () => { process.stderr.write("native failure"); process.exitCode = 9; });', Buffer.alloc(0))
    expect(result.code).toBe(9)
    expect(result.stderr).toContain('native failure')
  })
})
