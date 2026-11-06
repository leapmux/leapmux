import type { RecoveryDirection, RecoveryMode } from './recoveryRelay'
import { Buffer } from 'node:buffer'
import { spawn } from 'node:child_process'
import { EventEmitter } from 'node:events'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { PassThrough, Readable, Writable } from 'node:stream'
import { finished } from 'node:stream/promises'
import { runInNewContext } from 'node:vm'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { stopProcess } from '../helpers/process'
import { createZcodeRecoveryTransformer, writeZcodeRecoveryRelay } from './recoveryRelay'

function fixture(mode: RecoveryMode = 'turn-end') {
  const relay = createZcodeRecoveryTransformer('selected-prompt', undefined, mode)
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
  it.each(['empty-response', 'stream-gap', 'reasoning-gap'] as const)('preserves %s eligibility after unrelated or incomplete events', (mode) => {
    const { native } = fixture(mode)
    const event = {
      sessionId: 's',
      turnId: 't',
      seq: 3,
      type: mode === 'empty-response'
        ? 'session.updated'
        : 'model.streaming',
      payload: mode === 'empty-response'
        ? { content: 'Answer.', querySource: 'main_turn', stopReason: 'stop' }
        : {
            kind: mode === 'reasoning-gap'
              ? 'reasoning_delta'
              : 'text_delta',
            delta: 'Answer.',
          },
    }
    for (const unrelated of [
      { ...event, sessionId: 'foreign' },
      { ...event, turnId: 'foreign' },
      { ...event, payload: null },
      { ...event, payload: [] },
      { ...event, payload: {} },
    ]) {
      const frame = { method: 'session/event', params: unrelated }
      expect(JSON.parse(native(frame)!)).toEqual(frame)
    }
    const frame = { method: 'session/event', params: event }
    expect(native(frame)).not.toBe(`${JSON.stringify(frame)}\n`)
    expect(JSON.parse(native(frame)!)).toEqual(frame)
  })

  it.each([false, true])('preserves the event envelope when nested is %s', (nested) => {
    const { native } = fixture('empty-response')
    const event = {
      sessionId: 's',
      turnId: 't',
      seq: 3,
      eventId: 'response',
      future: 7,
      type: 'session.updated',
      payload: {
        content: 'Answer.',
        querySource: 'main_turn',
        stopReason: 'stop',
        usage: { inputTokens: 1 },
      },
    }
    const params = nested
      ? { sessionId: 's', outer: 'retained', event }
      : event
    const changed = {
      ...event,
      payload: { ...event.payload, content: '' },
    }
    expect(JSON.parse(native({ method: 'session/event', frameMetadata: 'retained', params })!)).toEqual({
      method: 'session/event',
      frameMetadata: 'retained',
      params: nested
        ? { ...params, event: changed }
        : changed,
    })
  })

  it.each([false, true])('preserves projected event metadata when nested is %s', (nested) => {
    const { native } = fixture()
    native({
      method: 'session/event',
      params: end(),
    })
    const event = {
      ...end(6, 's', 'next'),
      future: 'retained',
    }
    const params = nested
      ? { sessionId: 's', outer: 'retained', event }
      : event
    const changed = { ...event, seq: 5 }
    expect(JSON.parse(native({ method: 'session/event', frameMetadata: 'retained', params })!)).toEqual({
      method: 'session/event',
      frameMetadata: 'retained',
      params: nested
        ? { ...params, event: changed }
        : changed,
    })
  })

  it('replaces only one selected completion with an empty response and preserves its native identity', () => {
    const { native } = fixture('empty-response')
    const response = {
      sessionId: 's',
      turnId: 't',
      seq: 3,
      eventId: 'response',
      type: 'session.updated',
      payload: {
        content: 'Final answer.',
        querySource: 'main_turn',
        stopReason: 'stop',
        usage: { inputTokens: 1 },
      },
    }
    const foreign = { ...response, turnId: 'other' }
    expect(JSON.parse(native({ method: 'session/event', params: foreign })!)).toEqual({ method: 'session/event', params: foreign })
    expect(JSON.parse(native({ method: 'session/event', params: response })!)).toEqual({
      method: 'session/event',
      params: {
        ...response,
        payload: { ...response.payload, content: '' },
      },
    })
    expect(JSON.parse(native({ method: 'session/event', params: response })!)).toEqual({ method: 'session/event', params: response })
    expect(JSON.parse(native({
      method: 'session/event',
      params: end(),
    })!)).toEqual({
      method: 'session/event',
      params: end(),
    })
  })

  it.each([
    ['stream-gap', 'text_delta'],
    ['reasoning-gap', 'reasoning_delta'],
  ] as const)('drops one selected live %s delta without changing replay or sequence numbers', (mode, kind) => {
    const { worker, native } = fixture(mode)
    const delta = {
      sessionId: 's',
      turnId: 't',
      seq: 3,
      type: 'model.streaming',
      payload: { kind, delta: 'first' },
    }
    const foreign = { ...delta, sessionId: 'other' }
    expect(JSON.parse(native({ method: 'session/event', params: foreign })!)).toEqual({ method: 'session/event', params: foreign })
    expect(native({ method: 'session/event', params: delta })).toBeNull()
    const next = {
      ...delta,
      seq: 4,
      payload: { kind, delta: 'second' },
    }
    expect(JSON.parse(native({ method: 'session/event', params: next })!)).toEqual({ method: 'session/event', params: next })
    const request = {
      id: 2,
      method: 'session/events',
      params: { sessionId: 's', afterSeq: 2 },
    }
    expect(JSON.parse(worker(request)!)).toEqual(request)
    const reply = {
      id: 2,
      result: {
        events: [
          delta,
          next,
          end(),
        ],
      },
    }
    expect(JSON.parse(native(reply)!)).toEqual(reply)
    expect(JSON.parse(native({
      method: 'session/event',
      params: end(),
    })!)).toEqual({
      method: 'session/event',
      params: end(),
    })
  })

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

async function runFixture(source: string, input: Buffer, argv = ['app-server', '--stdio'], mode: RecoveryMode = 'turn-end') {
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
    mode,
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
  it('runs nested response replacement from the serialized transformer', async () => {
    const start = {
      method: 'session/event',
      params: {
        sessionId: 's',
        turnId: 't',
        seq: 1,
        type: 'turn.started',
        payload: { inputId: 'i' },
      },
    }
    const event = {
      sessionId: 's',
      turnId: 't',
      seq: 2,
      type: 'session.updated',
      payload: { content: 'Answer.', querySource: 'main_turn', stopReason: 'stop' },
    }
    const response = {
      method: 'session/event',
      metadata: 'retained',
      params: { sessionId: 's', outer: 'retained', event },
    }
    const source = `process.stdin.resume(); process.stdin.on('end', () => process.stdout.write(${JSON.stringify(`${JSON.stringify(start)}\n${JSON.stringify(response)}\n`)}));`
    const result = await runFixture(source, Buffer.from('{"id":1,"method":"session/send","params":{"sessionId":"s","inputId":"i","content":"marker"}}\n'), undefined, 'empty-response')
    expect(result.code, result.stderr).toBe(0)
    expect(result.output.toString().trim().split('\n').map(line => JSON.parse(line))).toEqual([
      start,
      {
        ...response,
        params: {
          ...response.params,
          event: {
            ...event,
            payload: { ...event.payload, content: '' },
          },
        },
      },
    ])
  })

  it('copies fragmented frames and final fragments in linear space', async () => {
    const relay = writeZcodeRecoveryRelay(directory, {
      launch: { binaryName: 'zcode', executable: process.execPath },
      marker: 'marker',
      evidencePath: join(directory, 'copy-evidence.jsonl'),
    })
    const chunks = Array.from({ length: 128 }, () => Buffer.alloc(64 * 1024, 120))
    chunks.push(Buffer.from('\n{"next":true}\nfinal fragment'))
    const expected = Buffer.concat(chunks)
    let copied = 0
    const countedBuffer = new Proxy(Buffer, {
      get(target, property) {
        if (property === 'concat') {
          return (parts: readonly Uint8Array[], length?: number) => {
            copied += length ?? parts.reduce((sum, part) => sum + part.byteLength, 0)
            return Buffer.concat(parts, length)
          }
        }
        if (property === 'from') {
          return (value: string | Uint8Array, encoding?: BufferEncoding) => {
            if (typeof value !== 'string') {
              copied += value.byteLength
              return Buffer.from(value)
            }
            return Buffer.from(value, encoding)
          }
        }
        return Reflect.get(target, property)
      },
    })
    const forwarded: Buffer[] = []
    const evidence: string[] = []
    const childInput = new Writable({
      write(chunk, _encoding, done) {
        forwarded.push(chunk)
        done()
      },
    })
    const child = Object.assign(new EventEmitter(), {
      stdin: childInput,
      stdout: Readable.from([]),
      stderr: new PassThrough(),
      kill() { throw new Error('The relay must not kill the fixture child.') },
      exitCode: null,
      signalCode: null,
    })
    const fixtureProcess = Object.assign(new EventEmitter(), {
      argv: [
        'node',
        relay.scriptPath,
        'app-server',
        '--stdio',
      ],
      stdin: Readable.from(chunks),
      stdout: new PassThrough(),
      stderr: new PassThrough(),
    })
    const modules: Record<string, unknown> = {
      'node:buffer': { Buffer: countedBuffer },
      'node:child_process': {
        spawn: () => child,
      },
      'node:fs': {
        appendFileSync: (_path: string, value: string) => evidence.push(value),
      },
    }
    const done = finished(childInput)
    runInNewContext(readFileSync(relay.scriptPath, 'utf8'), {
      require: (name: string) => {
        if (!(name in modules))
          throw new Error(`Unexpected relay module: ${name}`)
        return modules[name]
      },
      process: fixtureProcess,
    })
    await done
    expect(Buffer.concat(forwarded).equals(expected)).toBe(true)
    expect(evidence.map(line => JSON.parse(line) as { raw: string, forwarded: string | null, fragment: boolean })).toEqual([
      {
        direction: 'worker',
        raw: Buffer.concat(chunks.slice(0, 128).concat([
          Buffer.from('\n'),
        ])).toString('base64'),
        forwarded: Buffer.concat(chunks.slice(0, 128).concat([
          Buffer.from('\n'),
        ])).toString('base64'),
        fragment: false,
      },
      {
        direction: 'worker',
        raw: Buffer.from('{"next":true}\n').toString('base64'),
        forwarded: Buffer.from('{"next":true}\n').toString('base64'),
        fragment: false,
      },
      {
        direction: 'worker',
        raw: Buffer.from('final fragment').toString('base64'),
        forwarded: Buffer.from('final fragment').toString('base64'),
        fragment: true,
      },
    ])
    expect(copied).toBeLessThanOrEqual(expected.length * 2)
  })

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
