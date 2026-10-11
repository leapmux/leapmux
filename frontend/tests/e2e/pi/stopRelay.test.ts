import type { PiStopRelayOptions } from './stopRelay'
import { Buffer } from 'node:buffer'
import { spawn } from 'node:child_process'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { connect } from 'node:net'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { stopProcess } from '../helpers/process'
import { createPiStopRelay, piStopRelayProgram, withPiRetrySignal } from './stopRelay'

let directory: string
const children: ReturnType<typeof spawn>[] = []

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  directory = mkdtempSync(join(scratch, 'pi-relay-unit-'))
})

afterEach(async () => {
  const results = await Promise.allSettled(children.splice(0).map(child => stopProcess(child)))
  const failures = results.filter(result => result.status === 'rejected')
  if (failures.length)
    throw new AggregateError(failures.map(result => result.reason), 'The Pi relay unit cleanup failed.')
  rmSync(directory, { recursive: true, force: true })
})

/** Run a real Node child. Its fixture output belongs only to this unit test. */
async function runFixture(source: string, input: string, options: Partial<PiStopRelayOptions> = {}, argv: string[] = []) {
  const native = join(directory, 'native child.cjs')
  const relay = join(directory, 'relay.cjs')
  writeFileSync(native, source)
  writeFileSync(relay, piStopRelayProgram({
    executable: process.execPath,
    args: [native],
    originalMarker: 'ORIGINAL',
    evidencePath: join(directory, 'evidence.jsonl'),
    ...options,
  }))
  const child = spawn(process.execPath, [relay, ...argv], { stdio: ['pipe', 'pipe', 'pipe'] })
  children.push(child)
  const output: Buffer[] = []
  const stderr: Buffer[] = []
  child.stdout.on('data', chunk => output.push(Buffer.from(chunk)))
  child.stderr.on('data', chunk => stderr.push(Buffer.from(chunk)))
  const result = new Promise<{ code: number | null, output: Buffer, stderr: Buffer }>((resolveResult, reject) => {
    child.once('error', reject)
    child.once('close', code => resolveResult({ code, output: Buffer.concat(output), stderr: Buffer.concat(stderr) }))
  })
  child.stdin.end(input)
  return result
}

describe('piStopRelayProgram', () => {
  it('does not hold a reply before the marked original prompt', async () => {
    const output = '{"type":"response","id":"leapmux-1","command":"abort","success":true}\n'
    const result = await runFixture(`process.stdin.resume(); process.stdin.on('end', () => process.stdout.write(${JSON.stringify(output)}));`, '{"type":"abort","id":"leapmux-1"}\n', { replacement: { prompt: 'REPLACEMENT', title: 'Question' } })
    expect(result.output.toString()).toBe(output)
    expect(result.code).toBe(0)
    expect(readFileSync(join(directory, 'evidence.jsonl'), 'utf8')).not.toContain('held-abort-reply')
  })

  it('retains a held reply and reports the absent replacement when the child ends', async () => {
    const output = '{"type":"response","id":"leapmux-1","command":"abort","success":true}\n'
    const result = await runFixture(`process.stdin.resume(); process.stdin.on('end', () => process.stdout.write(${JSON.stringify(output)}));`, '{"type":"prompt","message":"ORIGINAL"}\n{"type":"abort","id":"leapmux-1"}\n', { replacement: { prompt: 'REPLACEMENT', title: 'Question' } })
    expect(result.output.toString()).toBe(output)
    expect(result.code).toBe(0)
    expect(readFileSync(join(directory, 'evidence.jsonl'), 'utf8')).toContain('unreleased-abort-reply')
  })

  it('reports a native spawn error and returns a failed exit', async () => {
    const result = await runFixture('', '', { executable: join(directory, 'absent-native') })
    expect(result.code).toBe(1)
    expect(result.output.length).toBe(0)
    expect(result.stderr.toString()).toContain('ENOENT')
  })

  it.each([
    { id: 'other', command: 'abort', success: true },
    { id: 'leapmux-1', command: 'prompt', success: true },
    { id: 'leapmux-1', command: 'abort', success: false },
    { id: 'leapmux-1', success: true },
    { command: 'abort', success: true },
  ])('preserves an unmatched native response %j', async (fields) => {
    const output = ` \t${JSON.stringify({ type: 'response', ...fields })}\r\n`
    const result = await runFixture(`process.stdin.resume(); process.stdin.on('end', () => process.stdout.write(${JSON.stringify(output)}));`, '{"type":"prompt","message":"ORIGINAL"}\n{"type":"abort","id":"leapmux-1"}\n', { replacement: { prompt: 'REPLACEMENT', title: 'Question' } })
    expect(result.code).toBe(0)
    expect(result.output.toString()).toBe(output)
    expect(readFileSync(join(directory, 'evidence.jsonl'), 'utf8')).not.toContain('held-abort-reply')
  })

  it('keeps malformed output, the unfinished output tail, stderr and a nonzero exit', async () => {
    const output = Buffer.from([123, 255, 10, 91, 93, 10, 195, 169])
    const result = await runFixture(`process.stdin.resume(); process.stdin.on('end', () => { process.stdout.write(Buffer.from(${JSON.stringify([...output])})); process.stderr.write('native stderr'); process.exitCode = 7; });`, '')
    expect(result.output).toEqual(output)
    expect(result.stderr.toString()).toBe('native stderr')
    expect(result.code).toBe(7)
  })

  it('passes every configured and runtime argument without shell interpretation', async () => {
    const argv = ['--flag', '', '$HOME', '한글 and spaces', 'quote"text']
    const result = await runFixture(`process.stdin.resume(); process.stdin.on('end', () => process.stdout.write(JSON.stringify(process.argv.slice(2))));`, '', {}, argv)
    expect(result.code).toBe(0)
    expect(JSON.parse(result.output.toString())).toEqual(argv)
  })

  it('signals only the first matching native retry start and preserves all output', async () => {
    const unrelated = '{"type":"auto_retry_start","errorMessage":"another turn"}\n'
    const start = ' {"type":"auto_retry_start","attempt":1,"delayMs":2000,"errorMessage":"500 ORIGINAL"}\r\n'
    let received!: (value: Buffer) => void
    const observed = new Promise<Buffer>((resolveValue) => {
      received = resolveValue
    })
    const signals: Buffer[] = []
    await withPiRetrySignal((bytes) => {
      signals.push(bytes)
      received(bytes)
    }, async (retrySignal) => {
      const result = await runFixture(`process.stdin.resume(); process.stdin.on('end', () => process.stdout.write(${JSON.stringify(unrelated + start + start)}));`, '', { retrySignal })
      expect(await observed).toEqual(Buffer.from(start))
      expect(result.output.toString()).toBe(unrelated + start + start)
      expect(result.code).toBe(0)
    })
    expect(signals).toEqual([Buffer.from(start)])
  })

  it('holds only the matched native reply and releases its unchanged bytes after the real dialog', async () => {
    const native = join(directory, 'native-fixture.cjs')
    const evidence = join(directory, 'evidence.jsonl')
    const reply = ' \t{"type":"response","id":"leapmux-1","command":"abort","success":true,"extra":0}\r\n'
    const unrelated = '{"type":"response","id":"different","command":"abort","success":true}\n'
    const dialog = '{"type":"extension_ui_request","id":"actual-dialog","method":"editor","title":"Replacement question","prefill":"한 text"}\n'
    writeFileSync(native, `
const readline = require('node:readline');
const input = readline.createInterface({ input: process.stdin });
input.on('line', line => {
  const frame = JSON.parse(line);
  if (frame.type === 'abort') { process.stdout.write(${JSON.stringify(unrelated)}); process.stdout.write(${JSON.stringify(reply)}); }
  if (frame.type === 'prompt' && frame.message === 'REPLACEMENT') {
    const bytes = Buffer.from(${JSON.stringify(dialog)});
    for (const byte of bytes) process.stdout.write(Buffer.from([byte]));
  }
});
input.on('close', () => process.exit(0));
`)
    const relay = join(directory, 'relay.cjs')
    writeFileSync(relay, piStopRelayProgram({ executable: process.execPath, args: [native], originalMarker: 'ORIGINAL', replacement: { prompt: 'REPLACEMENT', title: 'Replacement question' }, evidencePath: evidence }))
    const proc = spawn(process.execPath, [relay], { stdio: ['pipe', 'pipe', 'pipe'] })
    children.push(proc)
    const output: Buffer[] = []
    const failures: Buffer[] = []
    const closed = new Promise<void>((resolveClose, reject) => {
      proc.once('error', reject)
      proc.once('close', code => code === 0 ? resolveClose() : reject(new Error(`Relay exited with ${code}: ${Buffer.concat(failures).toString()}`)))
    })
    proc.stderr.on('data', chunk => failures.push(Buffer.from(chunk)))
    const delivered = new Promise<void>((resolveDelivery) => {
      proc.stdout.on('data', (chunk) => {
        output.push(Buffer.from(chunk))
        if (Buffer.concat(output).includes(Buffer.from(reply)))
          resolveDelivery()
      })
    })
    try {
      const input = Buffer.from('{"type":"prompt","message":"ORIGINAL"}\n{"type":"abort","id":"leapmux-1"}\n')
      for (const byte of input)
        proc.stdin.write(Buffer.from([byte]))
      await delivered
      proc.stdin.end()
      await closed
      expect(Buffer.concat(output).toString()).toBe(unrelated + dialog + reply)
      const records = readFileSync(evidence, 'utf8').trim().split('\n').map(line => JSON.parse(line) as { kind: string, bytes: string })
      expect(records.filter(record => record.kind !== 'native-output' && record.kind !== 'native-process').map(record => record.kind)).toEqual(['abort-input', 'held-abort-reply', 'replacement-input', 'replacement-dialog', 'released-abort-reply'])
      const held = records.find(record => record.kind === 'held-abort-reply')
      const released = records.find(record => record.kind === 'released-abort-reply')
      if (!held || !released)
        throw new Error('The relay did not record both native reply stages.')
      expect(held.bytes).toBe(released.bytes)
      expect(Buffer.from(held.bytes, 'base64').toString()).toBe(reply)
    }
    finally {
      if (proc.exitCode === null)
        proc.kill('SIGTERM')
      await closed
    }
  })
})

describe('createPiStopRelay', () => {
  it('uses the shared Node launcher with the platform extension', () => {
    const launcher = createPiStopRelay(directory, { executable: process.execPath, originalMarker: 'ORIGINAL', evidencePath: join(directory, 'evidence.jsonl') })
    expect(launcher).toBe(join(directory, process.platform === 'win32' ? 'pi.cmd' : 'pi'))
    expect(readFileSync(launcher, 'utf8')).toContain(process.execPath)
    expect(readFileSync(launcher, 'utf8')).toContain(join(directory, 'stop-relay.cjs'))
  })

  it.each(['executable', 'originalMarker', 'evidencePath'] as const)('rejects an empty %s', (key) => {
    expect(() => createPiStopRelay(directory, { executable: process.execPath, originalMarker: 'ORIGINAL', evidencePath: join(directory, 'evidence.jsonl'), [key]: '' })).toThrow('complete scenario values')
  })

  it.each([0, -1, 65536, 1.5, Number.MAX_SAFE_INTEGER])('rejects the invalid signal port %s', (port) => {
    expect(() => createPiStopRelay(directory, { executable: process.execPath, originalMarker: 'ORIGINAL', evidencePath: join(directory, 'evidence.jsonl'), retrySignal: { port, nonce: 'a'.repeat(64) } })).toThrow('valid port and private nonce')
  })

  it('rejects an absent private nonce', () => {
    expect(() => createPiStopRelay(directory, { executable: process.execPath, originalMarker: 'ORIGINAL', evidencePath: join(directory, 'evidence.jsonl'), retrySignal: { port: 12345, nonce: '' } })).toThrow('valid port and private nonce')
  })
})

describe('withPiRetrySignal', () => {
  it('rejects every later packet in the same chunk after malformed json', async () => {
    let calls = 0
    await expect(withPiRetrySignal(() => {
      calls++
    }, async (signal) => {
      await new Promise<void>((resolveClosed, reject) => {
        const socket = connect(signal.port, '127.0.0.1')
        socket.once('error', reject)
        socket.once('connect', () => socket.end(`invalid\n${JSON.stringify({ nonce: signal.nonce, bytes: Buffer.from('{"type":"auto_retry_start"}\n').toString('base64') })}\n`))
        socket.once('close', () => resolveClosed())
      })
    })).rejects.toThrow(SyntaxError)
    expect(calls).toBe(0)
  })

  it.each([
    { packet: 'null', expected: 'invalid identity or payload' },
    { packet: '{}', expected: 'invalid identity or payload' },
    { packet: '{"nonce":"other","bytes":"eA=="}', expected: 'invalid identity or payload' },
    { packet: 'invalid', expected: SyntaxError },
  ])('rejects a malformed signal $packet and closes its socket', async ({ packet, expected }) => {
    await expect(withPiRetrySignal(() => {
      throw new Error('An invalid signal must not reach the handler.')
    }, async (signal) => {
      await new Promise<void>((resolveClosed, reject) => {
        const socket = connect(signal.port, '127.0.0.1')
        socket.once('error', reject)
        socket.once('connect', () => socket.end(`${packet}\n`))
        socket.once('close', () => resolveClosed())
      })
    })).rejects.toThrow(expected)
  })
})
