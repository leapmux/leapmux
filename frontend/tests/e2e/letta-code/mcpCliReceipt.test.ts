import type { ChildProcessWithoutNullStreams } from 'node:child_process'
import type { LettaMcpCliCapture, LettaMcpCliIdentity, LettaMcpCliReceipt } from './mcpCliReceipt'
import { Buffer } from 'node:buffer'
import { spawn } from 'node:child_process'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { stopProcess } from '../helpers/process'
import { isAlive } from '../helpers/processTree'
import { waitForFileSignal } from '../helpers/toolOutputControl'
import { parseLettaMcpCatalog } from './mcpCatalog'
import { readLettaMcpCliReceipt, writeLettaMcpCliCapture } from './mcpCliReceipt'

let directory: string
const children: ChildProcessWithoutNullStreams[] = []

beforeEach(() => {
  const scratch = resolve(process.cwd(), '../.tmp')
  mkdirSync(scratch, { recursive: true })
  directory = mkdtempSync(join(scratch, 'letta-mcp-cli-receipt-'))
})
afterEach(async () => {
  await Promise.all(children.splice(0).map(child => stopProcess(child)))
  rmSync(directory, { recursive: true, force: true })
})

function runCapture(capture: LettaMcpCliCapture, args: string[], executable = process.execPath) {
  const expected: LettaMcpCliIdentity = { receiptId: capture.receiptId, callId: 'catalog-native', executable, args }
  const child = spawn(process.execPath, [capture.scriptPath, expected.receiptId, expected.callId, executable, ...args], {
    cwd: directory,
    env: { ...process.env, LETTA_CAPTURE_ENV: 'private environment 한글' },
    stdio: ['pipe', 'pipe', 'pipe'],
  })
  children.push(child)
  const stdout: Buffer[] = []
  const stderr: Buffer[] = []
  child.stdout.on('data', chunk => stdout.push(chunk))
  child.stderr.on('data', chunk => stderr.push(chunk))
  const closed = new Promise<{ code: number | null, signal: NodeJS.Signals | null, nativeResult: string }>((resolve, reject) => {
    child.once('error', reject)
    child.once('close', (code, signal) => resolve({
      code,
      signal,
      nativeResult: `${Buffer.concat(stdout).toString('utf8')}\n${Buffer.concat(stderr).toString('utf8')}`,
    }))
  })
  return { child, closed, expected }
}

describe('writeLettaMcpCliCapture', () => {
  it('preserves actual argv and isolated environment while separating UTF8 output and warnings', async () => {
    const native = join(directory, 'native argv "한글".cjs')
    writeFileSync(native, `
const fs = require('node:fs')
const output = Buffer.from(JSON.stringify({ args: process.argv.slice(2), environment: process.env.LETTA_CAPTURE_ENV, value: '한글' }) + '\\n')
const split = output.indexOf(Buffer.from('한글')) + 1
fs.writeSync(1, output.subarray(0, split))
setImmediate(() => {
  fs.writeSync(1, output.subarray(split))
  fs.writeSync(2, 'ACTUAL_STDERR_WARNING_한글\\n')
})
`)
    const capture = writeLettaMcpCliCapture(directory)
    const args = [native, '', '0', 'false', 'quote " \' \\ 한글', '$(touch forbidden);`echo forbidden`']
    const operation = runCapture(capture, args)
    const result = await operation.closed
    expect(result.code).toBe(0)
    expect(result.signal).toBeNull()
    const receipt = readLettaMcpCliReceipt(capture.receiptPath, result.nativeResult, operation.expected)
    expect(JSON.parse(receipt.stdout)).toEqual({ args: args.slice(1), environment: 'private environment 한글', value: '한글' })
    expect(receipt.stderr).toBe('ACTUAL_STDERR_WARNING_한글\n')
    expect(result.nativeResult).toContain(receipt.stdout)
    expect(result.nativeResult).toContain(receipt.stderr)
    expect(receipt.exitCode).toBe(0)
    expect(receipt.signal).toBeNull()
    expect(receipt.spawnError).toBeNull()
  })

  it('propagates a failed native exit without treating valid stdout as a successful catalog', async () => {
    const capture = writeLettaMcpCliCapture(directory)
    const operation = runCapture(capture, ['-e', 'process.stdout.write(\'[{"name":"echo","inputSchema":{}}]\');process.stderr.write("ACTUAL_ERROR");process.exitCode=7'])
    const result = await operation.closed
    expect(result.code).toBe(7)
    const receipt = readLettaMcpCliReceipt(capture.receiptPath, result.nativeResult, operation.expected)
    expect(receipt.exitCode).toBe(7)
    expect(receipt.stderr).toBe('ACTUAL_ERROR')
    expect(() => parseLettaMcpCatalog(receipt)).toThrow('successful CLI receipt')
  })

  it('retains the spawn failure receipt and exit status for an absent executable', async () => {
    const capture = writeLettaMcpCliCapture(directory)
    const operation = runCapture(capture, [], join(directory, 'absent-native'))
    const result = await operation.closed
    expect(result.code).toBe(127)
    const receipt = readLettaMcpCliReceipt(capture.receiptPath, result.nativeResult, operation.expected)
    expect(receipt.exitCode).toBe(127)
    expect(receipt.spawnError).toContain('ENOENT')
    expect(receipt.stdout).toBe('')
    expect(receipt.stderr).toBe('')
    expect(() => parseLettaMcpCatalog(receipt)).toThrow('successful CLI receipt')
  })

  it('forwards shutdown to the native child and preserves its actual signal after physical exit', async () => {
    const ready = join(directory, 'native-ready')
    const native = join(directory, 'native-held.cjs')
    writeFileSync(native, `const fs=require('node:fs');const subscription=fs.watch(${JSON.stringify(directory)},()=>{});fs.writeFileSync(${JSON.stringify(ready)},String(process.pid))`)
    const capture = writeLettaMcpCliCapture(directory)
    const operation = runCapture(capture, [native])
    await waitForFileSignal(ready)
    const pid = Number(readFileSync(ready, 'utf8'))
    expect(Number.isSafeInteger(pid) && pid > 0).toBe(true)
    expect(operation.child.kill('SIGTERM')).toBe(true)
    const result = await operation.closed
    expect(result.signal).toBe('SIGTERM')
    expect(result.code).toBeNull()
    expect(isAlive(pid)).toBe(false)
    const receipt = readLettaMcpCliReceipt(capture.receiptPath, result.nativeResult, operation.expected)
    expect(receipt.signal).toBe('SIGTERM')
    expect(receipt.exitCode).toBeNull()
    expect(() => parseLettaMcpCatalog(receipt)).toThrow('successful CLI receipt')
  })

  it('does not replace an existing receipt when the command runs again', async () => {
    const capture = writeLettaMcpCliCapture(directory)
    const operation = runCapture(capture, ['-e', 'process.stdout.write("FIRST_NATIVE_OUTPUT")'])
    const first = await operation.closed
    const before = readFileSync(capture.receiptPath, 'utf8')
    expect(readLettaMcpCliReceipt(capture.receiptPath, first.nativeResult, operation.expected).stdout).toBe('FIRST_NATIVE_OUTPUT')
    const second = await runCapture(capture, ['-e', 'process.stdout.write("SECOND_NATIVE_OUTPUT")']).closed
    expect(second.code).toBe(1)
    expect(second.nativeResult).toContain('could not be published')
    expect(readFileSync(capture.receiptPath, 'utf8')).toBe(before)
    expect(() => readLettaMcpCliReceipt(capture.receiptPath, second.nativeResult, operation.expected)).toThrow('one complete CLI receipt marker')
  })

  it('publishes exactly one complete receipt when two native invocations finish concurrently', async () => {
    const capture = writeLettaMcpCliCapture(directory)
    const operations = ['FIRST', 'SECOND'].map(value => runCapture(capture, ['-e', `process.stdout.write(${JSON.stringify(value)})`]))
    const results = await Promise.all(operations.map(operation => operation.closed))
    expect(results.map(result => result.code).sort()).toEqual([0, 1])
    const winnerIndex = results.findIndex(result => result.code === 0)
    const winner = results[winnerIndex]
    const operation = operations[winnerIndex]
    if (!winner || !operation)
      throw new Error('The concurrent native captures produced no successful receipt.')
    const receipt = readLettaMcpCliReceipt(capture.receiptPath, winner.nativeResult, operation.expected)
    expect(receipt.stdout).toBe(winnerIndex === 0 ? 'FIRST' : 'SECOND')
  })

  it('preserves large native output without splitting its UTF8 characters', async () => {
    const capture = writeLettaMcpCliCapture(directory)
    const operation = runCapture(capture, ['-e', 'process.stdout.write("한글".repeat(100000));process.stderr.write("warn".repeat(100000))'])
    const result = await operation.closed
    const receipt = readLettaMcpCliReceipt(capture.receiptPath, result.nativeResult, operation.expected)
    expect(result.code).toBe(0)
    expect(receipt.stdout).toBe('한글'.repeat(100_000))
    expect(receipt.stderr).toBe('warn'.repeat(100_000))
  })
  it('rejects a relative capture directory before it creates any file', () => {
    expect(() => writeLettaMcpCliCapture('relative')).toThrow('absolute private directory')
  })
})

describe('readLettaMcpCliReceipt', () => {
  const identity: LettaMcpCliIdentity = { receiptId: 'actual-receipt', callId: 'catalog-native', executable: process.execPath, args: ['mcp', 'tools', 'echo', '--agent', 'agent-native'] }
  const receipt: LettaMcpCliReceipt = { ...identity, stdout: '', stderr: '', exitCode: 0, signal: null, spawnError: null }
  const marker = `LETTAMCPCLI:${identity.receiptId}:${identity.callId}`
  const nativeResult = `native stdout\n${marker}\nnative stderr`

  function writeReceipt(value: unknown): string {
    const path = join(directory, 'receipt.json')
    writeFileSync(path, `${JSON.stringify(value)}\n`)
    return path
  }

  it('retains empty output streams and the exact zero exit status', () => {
    expect(readLettaMcpCliReceipt(writeReceipt(receipt), nativeResult, identity)).toEqual(receipt)
  })
  it.each([
    { receiptId: 'another-receipt' },
    { callId: 'another-call' },
    { executable: resolve(process.cwd(), 'another-executable') },
    { args: [...identity.args, 'extra'] },
  ])('rejects an altered native invocation identity: %j', (overrides) => {
    expect(() => readLettaMcpCliReceipt(writeReceipt({ ...receipt, ...overrides }), nativeResult, identity)).toThrow('expected native invocation')
  })
  it.each(['', marker.slice(1), `${marker}\n${marker}`, `prefix${marker}\n`, `${marker}suffix\n`])('rejects a missing, partial, or duplicate model result marker: %j', (result) => {
    expect(() => readLettaMcpCliReceipt(writeReceipt(receipt), result, identity)).toThrow('one complete CLI receipt marker')
  })
  it.each([
    null,
    '',
    '[]',
    'Exit code: 0\n[{"name":"echo","inputSchema":{}}]',
    [],
    {},
    { ...receipt, stdout: null },
    { ...receipt, stderr: false },
    { ...receipt, args: [false] },
    { ...receipt, exitCode: -1 },
    { ...receipt, exitCode: false },
    { ...receipt, exitCode: Number.POSITIVE_INFINITY },
    { ...receipt, signal: false },
    { ...receipt, signal: '' },
    { ...receipt, signal: 'SIG_INVALID' },
    { ...receipt, spawnError: '' },
    { ...receipt, spawnError: false },
    { ...receipt, unknown: 0 },
    { ...receipt, spawnError: undefined },
  ])('rejects a partial or malformed native receipt: %j', (value) => {
    expect(() => readLettaMcpCliReceipt(writeReceipt(value), nativeResult, identity)).toThrow('incomplete or malformed')
  })
  it.each(['{', `${JSON.stringify(receipt)}\n${JSON.stringify(receipt)}\n`])('rejects partial or duplicate serialized receipts: %j', (bytes) => {
    const path = join(directory, 'receipt.json')
    writeFileSync(path, bytes)
    expect(() => readLettaMcpCliReceipt(path, nativeResult, identity)).toThrow()
  })
  it('rejects a receipt symlink instead of reading another file', () => {
    const original = writeReceipt(receipt)
    const link = join(directory, 'linked-receipt.json')
    symlinkSync(original, link)
    expect(() => readLettaMcpCliReceipt(link, nativeResult, identity)).toThrow('actual private file')
  })
})
