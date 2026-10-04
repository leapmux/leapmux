import type { ChildProcessWithoutNullStreams } from 'node:child_process'
import type { Server } from 'node:http'
import type { KimiCatalogCapture, KimiCatalogOwner, KimiCatalogReceipt, KimiCatalogTool } from './toolCatalog'
import { Buffer } from 'node:buffer'
import { spawn } from 'node:child_process'
import { once } from 'node:events'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, realpathSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { createServer } from 'node:http'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { findBinary } from '../helpers/binaryOnPath'
import { stopProcess } from '../helpers/process'
import { assertKimiCatalogOwnership, assertKimiShellCatalog, createKimiCatalogCapture, parseKimiCatalogReadyLine, parseKimiCatalogReceipt, parseKimiCompleteCatalog, queryKimiCompleteCatalog, readKimiNativeHeader } from './toolCatalog'

function tool(change: Partial<KimiCatalogTool> = {}): KimiCatalogTool {
  return { name: 'Bash', description: 'Run a shell command.', input_schema: null, source: 'builtin', active: true, ...change }
}

function envelope(tools: unknown[] = [tool()]) {
  return { code: 0, msg: '', data: { tools }, request_id: 'actual-request' }
}

describe('readKimiNativeHeader', () => {
  it('reads at most 512 bytes and closes the exact descriptor even when it is zero', () => {
    const close = vi.fn()
    const open = vi.fn(() => 0)
    const read = vi.fn((descriptor: number, buffer: Buffer) => {
      expect(descriptor).toBe(0)
      expect(buffer.length).toBe(512)
      return buffer.write('#!/usr/bin/env node\nNative body.')
    })
    expect(readKimiNativeHeader('/actual/native/kimi', { open, read, close })).toBe('#!/usr/bin/env node')
    expect(open).toHaveBeenCalledWith('/actual/native/kimi')
    expect(close).toHaveBeenCalledExactlyOnceWith(0)
  })

  it('preserves an empty header and still closes its descriptor', () => {
    const close = vi.fn()
    expect(readKimiNativeHeader('/actual/native/kimi', { open: () => 7, read: () => 0, close })).toBe('')
    expect(close).toHaveBeenCalledExactlyOnceWith(7)
  })

  it('retains a read failure and closes the actual descriptor', () => {
    const cause = new Error('The native header read failed.')
    const close = vi.fn()
    expect(() => readKimiNativeHeader('/actual/native/kimi', { open: () => 7, read: () => {
      throw cause
    }, close })).toThrow(cause)
    expect(close).toHaveBeenCalledExactlyOnceWith(7)
  })

  it('retains the primary read failure when closing the same descriptor also fails', () => {
    const readCause = new Error('The native header read failed.')
    const closeCause = new Error('The native header close failed.')
    expect.assertions(2)
    try {
      readKimiNativeHeader('/actual/native/kimi', {
        open: () => 7,
        read: () => {
          throw readCause
        },
        close: () => {
          throw closeCause
        },
      })
    }
    catch (error) {
      expect(error).toBeInstanceOf(AggregateError)
      expect(error instanceof AggregateError ? error.errors : null).toEqual([readCause, closeCause])
    }
  })

  it('returns a close failure after the header read succeeds', () => {
    const cause = new Error('The native header close failed.')
    expect(() => readKimiNativeHeader('/actual/native/kimi', {
      open: () => 0,
      read: () => 0,
      close: () => {
        throw cause
      },
    })).toThrow(cause)
  })
})

describe('parseKimiCompleteCatalog', () => {
  it('keeps every active and inactive registration, its null schema, and its exact source', () => {
    const input = [tool(), tool({ name: 'Read', active: false }), tool({ name: 'native_출력', source: 'mcp', mcp_server_id: 'server-한글' }), tool({ name: 'SkillTool', source: 'skill' })]
    expect(parseKimiCompleteCatalog(envelope(input))).toEqual(input)
    expect(parseKimiCompleteCatalog(envelope(input))[1]?.active).toBe(false)
  })

  it.each([
    null,
    {},
    { ...envelope(), code: 7 },
    { ...envelope(), request_id: '' },
    { ...envelope(), data: {} },
    envelope([]),
    envelope([tool({ source: 'skill' })]),
    envelope([tool(), tool()]),
  ])('rejects an incomplete or failed native registry reply: %j', (input) => {
    expect(() => parseKimiCompleteCatalog(input)).toThrow()
  })

  it.each([
    { name: '' },
    { description: '' },
    { input_schema: undefined },
    { input_schema: {} },
    { active: undefined },
    { active: 0 },
    { source: 'unknown' },
    { mcp_server_id: '' },
    { mcp_server_id: null },
  ])('rejects a malformed native descriptor: %j', (change) => {
    expect(() => parseKimiCompleteCatalog(envelope([{ ...tool(), ...change }]))).toThrow('incomplete')
  })
})

describe('parseKimiCatalogReadyLine', () => {
  it('reads the exact loopback origin and opaque token from a native ready line', () => {
    expect(parseKimiCatalogReadyLine('Kimi server: http://127.0.0.1:43125/#token=native_한글')).toEqual({ origin: 'http://127.0.0.1:43125', token: 'native_한글' })
    expect(parseKimiCatalogReadyLine('Kimi server: http://127.0.0.1:43125#token=actual')).toEqual({ origin: 'http://127.0.0.1:43125', token: 'actual' })
    expect(parseKimiCatalogReadyLine('An unrelated native log line.')).toBeUndefined()
  })

  it.each([
    'Kimi server: http://127.0.0.1:43125/',
    'Kimi server: http://127.0.0.1:43125/#token=',
    'Kimi server: http://127.0.0.1:43125/#token=private token',
    'Kimi server: http://external.example:43125/#token=secret',
    'Kimi server: http://user:password@127.0.0.1:43125/#token=secret',
    'Kimi server: http://127.0.0.1:0/#token=secret',
    'Kimi server: https://127.0.0.1:43125/#token=secret',
  ])('rejects a malformed or unowned native origin without exposing the bearer token', (line) => {
    expect(() => parseKimiCatalogReadyLine(line)).toThrow()
    try {
      parseKimiCatalogReadyLine(line)
    }
    catch (error) {
      expect(error).toBeInstanceOf(Error)
      expect(error instanceof Error ? error.message : '').not.toContain('secret')
    }
  })
})

const launch = { nonce: 'actual-nonce', executable: '/native/kimi', launchArgs: [] }
const runtimeArgs = ['web', '--no-open', '--host', '127.0.0.1', '--port', '0', '--log-level', 'warn']

function receipt(change: Partial<KimiCatalogReceipt> = {}): KimiCatalogReceipt {
  return { nonce: launch.nonce, executable: launch.executable, nativePid: 30, wrapperPid: 20, argv: [...runtimeArgs], workingDir: '/project/private-agent', home: '/project/private-home', origin: 'http://127.0.0.1:43125', token: 'opaque-native-token', ...change }
}

describe('parseKimiCatalogReceipt', () => {
  it('requires the actual nonce, executable, and exact Worker server arguments', () => {
    expect(parseKimiCatalogReceipt(receipt(), launch)).toEqual(receipt())
  })

  it.each([
    { nonce: 'wrong-nonce' },
    { executable: '/other/kimi' },
    { nativePid: 0 },
    { nativePid: -1 },
    { nativePid: 1.5 },
    { nativePid: Number.MAX_SAFE_INTEGER + 1 },
    { nativePid: 20 },
    { wrapperPid: 0 },
    { workingDir: '' },
    { home: '' },
    { token: '' },
    { token: 'invalid token' },
    { origin: 'http://external.example:43125' },
    { argv: ['web', '--host', '127.0.0.1'] },
  ])('rejects an incomplete or changed native capture identity: %j', (change) => {
    expect(() => parseKimiCatalogReceipt(receipt(change), launch)).toThrow()
  })
})

const workerExecutable = '/project/.tmp/bin/leapmux'
const workerDataDir = '/project/.tmp/private worker'
const processRows = [
  { pid: 10, ppid: 1, command: `${workerExecutable} worker --hub http://127.0.0.1:1 --data-dir ${workerDataDir}` },
  { pid: 20, ppid: 10, command: `node /project/.tmp/wrapper/kimi-catalog.cjs ${runtimeArgs.join(' ')}` },
  { pid: 30, ppid: 20, command: `node /native/kimi ${runtimeArgs.join(' ')}` },
  { pid: 40, ppid: 10, command: '/native/unrelated-agent' },
]

const ownershipCapture: KimiCatalogOwner['capture'] = {
  executable: launch.executable,
  launchArgs: [],
  scriptPath: '/project/.tmp/wrapper/kimi-catalog.cjs',
  runtimeExecutable: '/native/node',
  runtimeInvocation: '/native/node',
  wrapperExecutable: '/native/node',
  nativeIsScript: true,
}

/** Pure ownership tests supply the observed executable for each exact fixture PID. */
function checkOwnership(value: KimiCatalogReceipt, rows: Parameters<typeof assertKimiCatalogOwnership>[1], executable = workerExecutable, dataDir = workerDataDir) {
  return assertKimiCatalogOwnership(value, rows, { workerExecutable: executable, workerDataDir: dataDir, capture: ownershipCapture }, async pid => rows.find(row => row.pid === pid)?.executable ?? '/native/node')
}

describe('assertKimiCatalogOwnership', () => {
  it('requires the captured native child, its actual wrapper, and the exact private Worker', async () => {
    await expect(checkOwnership(receipt(), processRows, workerExecutable, workerDataDir)).resolves.toBeUndefined()
  })

  it('rejects a different native child parent and a missing wrapper', async () => {
    await expect(checkOwnership(receipt(), processRows.map(row => row.pid === 30 ? { ...row, ppid: 40 } : row), workerExecutable, workerDataDir)).rejects.toThrow('wrapper')
    await expect(checkOwnership(receipt(), processRows.filter(row => row.pid !== 20), workerExecutable, workerDataDir)).rejects.toThrow('wrapper')
  })

  it('rejects another private Worker directory, a directory prefix match, and a duplicate process ID', async () => {
    await expect(checkOwnership(receipt(), processRows, workerExecutable, `${workerDataDir}-other`)).rejects.toThrow('exact private Worker')
    await expect(checkOwnership(receipt(), processRows, workerExecutable, '/project/.tmp/private')).rejects.toThrow('exact private Worker')
    await expect(checkOwnership(receipt(), [...processRows, processRows[2]!], workerExecutable, workerDataDir)).rejects.toThrow('distinct')
  })

  it('rejects an absent Worker boundary and a process cycle', async () => {
    await expect(checkOwnership(receipt(), processRows.filter(row => row.pid !== 10), workerExecutable, workerDataDir)).rejects.toThrow('boundary')
    await expect(checkOwnership(receipt(), processRows.map(row => row.pid === 20 ? { ...row, ppid: 30 } : row), workerExecutable, workerDataDir)).rejects.toThrow('cycle')
  })

  it('rejects another native executable at the captured PID', async () => {
    const rows = processRows.map(row => row.pid === 30 ? { ...row, executable: '/native/unrelated-runtime', command: '/native/unrelated-runtime web' } : row)
    await expect(checkOwnership(receipt(), rows, workerExecutable, workerDataDir)).rejects.toThrow()
  })

  it('rejects a native process command that omits the exact captured CLI script', async () => {
    const rows = processRows.map(row => row.pid === 30 ? { ...row, command: 'node /native/unrelated-kimi.mjs web' } : row)
    await expect(checkOwnership(receipt(), rows, workerExecutable, workerDataDir)).rejects.toThrow()
  })

  it('rejects another wrapper command under the same private Worker', async () => {
    const rows = processRows.map(row => row.pid === 20 ? { ...row, command: 'node unrelated-capture.cjs' } : row)
    await expect(checkOwnership(receipt(), rows, workerExecutable, workerDataDir)).rejects.toThrow()
  })

  it('rejects another physical executable even when both process commands match', async () => {
    const owner = { workerExecutable, workerDataDir, capture: ownershipCapture }
    await expect(assertKimiCatalogOwnership(receipt(), processRows, owner, async pid => pid === 30 ? '/native/unrelated-runtime' : '/native/node')).rejects.toThrow('physical executable')
    await expect(assertKimiCatalogOwnership(receipt(), processRows, owner, async pid => pid === 20 ? '/native/unrelated-wrapper' : '/native/node')).rejects.toThrow('physical executable')
  })

  it('retains an actual physical executable query failure', async () => {
    const owner = { workerExecutable, workerDataDir, capture: ownershipCapture }
    const cause = new Error('The exact native PID disappeared.')
    await expect(assertKimiCatalogOwnership(receipt(), processRows, owner, async () => {
      throw cause
    })).rejects.toBe(cause)
  })

  it('accepts an exact interpreter alias only when its physical executable matches', async () => {
    const rows = processRows.map(row => row.pid === 30 ? { ...row, command: `/native/alias/node /native/kimi ${runtimeArgs.join(' ')}` } : row)
    const owner = { workerExecutable, workerDataDir, capture: { ...ownershipCapture, runtimeInvocation: '/native/alias/node' } }
    await expect(assertKimiCatalogOwnership(receipt(), rows, owner, async () => '/native/node')).resolves.toBeUndefined()
    await expect(assertKimiCatalogOwnership(receipt(), rows, owner, async pid => pid === 30 ? '/native/other-node' : '/native/node')).rejects.toThrow('physical executable')
  })

  it('accepts the actual native process title after physical executable and wrapper ownership checks', async () => {
    const rows = processRows.map(row => row.pid === 30 ? { ...row, command: 'kimi-code' } : row)
    await expect(checkOwnership(receipt(), rows)).resolves.toBeUndefined()
  })

  it('rejects another process title and a forged native title with another physical executable', async () => {
    const wrongTitle = processRows.map(row => row.pid === 30 ? { ...row, command: 'kimi-code-other' } : row)
    await expect(checkOwnership(receipt(), wrongTitle)).rejects.toThrow('process title')
    const forged = processRows.map(row => row.pid === 30 ? { ...row, command: 'kimi-code', executable: '/native/unrelated-node' } : row)
    await expect(checkOwnership(receipt(), forged)).rejects.toThrow('physical executable')
  })
})

describe('assertKimiShellCatalog', () => {
  it('includes the actual media reader and the known private echo fixture in the complete registry', () => {
    expect(() => assertKimiShellCatalog([
      tool(),
      tool({ name: 'ReadMediaFile', description: 'Read a media file.' }),
      tool({ name: 'mcp__echo_probe__echo', source: 'mcp', mcp_server_id: 'echo_probe', description: 'Return a text message.' }),
    ])).not.toThrow()
  })

  it('rejects another MCP capability or a changed echo server identity', () => {
    expect(() => assertKimiShellCatalog([tool(), tool({ name: 'mcp__native_runner__execute', source: 'mcp', mcp_server_id: 'native_runner' })])).toThrow('unaudited')
    expect(() => assertKimiShellCatalog([tool(), tool({ name: 'mcp__echo_probe__echo', source: 'mcp', mcp_server_id: 'another-server' })])).toThrow('unaudited')
  })

  it('includes inactive audited registrations and requires the actual active shell tool', () => {
    expect(() => assertKimiShellCatalog([tool(), tool({ name: 'Read', active: false }), tool({ name: 'EnterPlanMode', active: false })])).not.toThrow()
  })

  it('rejects a new executor even when the model omits its inactive descriptor', () => {
    expect(() => assertKimiShellCatalog([tool(), tool({ name: 'native-script-runtime', description: 'Run JavaScript source.', active: false })])).toThrow('unaudited')
  })

  it.each([
    { tools: [] },
    { tools: [tool({ active: false })] },
    { tools: [tool({ source: 'mcp' })] },
    { tools: [tool(), tool({ name: 'unknown', source: 'skill' })] },
  ])('rejects an empty, inactive-shell, or external tool inventory: %j', ({ tools }) => {
    expect(() => assertKimiShellCatalog(tools)).toThrow()
  })
})

let directory: string
const children: ChildProcessWithoutNullStreams[] = []
const servers: Server[] = []

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  directory = mkdtempSync(join(scratch, 'kimi-catalog-transport-'))
})

afterEach(async () => {
  await Promise.all(children.splice(0).map(child => stopProcess(child)))
  await Promise.all(servers.splice(0).map(server => new Promise<void>((resolve, reject) => server.close(error => error ? reject(error) : resolve()))))
  rmSync(directory, { recursive: true, force: true })
})

function fixture(nativeSource: string, beforeStart?: (capture: KimiCatalogCapture) => void): { capture: KimiCatalogCapture, child: ChildProcessWithoutNullStreams, output: () => string, errors: () => string } {
  const native = join(directory, 'native.cjs')
  writeFileSync(native, nativeSource)
  const capture = createKimiCatalogCapture(join(directory, 'wrapper'), { binaryName: 'kimi', executable: process.execPath, args: [native] })
  beforeStart?.(capture)
  const child = spawn(process.execPath, [capture.scriptPath, ...runtimeArgs], { cwd: directory, env: { PATH: process.env.PATH, HOME: directory }, stdio: ['pipe', 'pipe', 'pipe'] })
  children.push(child)
  const output: Buffer[] = []
  let errors = ''
  child.stdout.on('data', (data: Buffer) => output.push(data))
  child.stderr.on('data', data => errors += data.toString('utf8'))
  return { capture, child, output: () => Buffer.concat(output).toString('utf8'), errors: () => errors }
}

describe('createKimiCatalogCapture', () => {
  it('identifies an absolute JavaScript interpreter from the native CLI header', () => {
    const interpreter = realpathSync(process.execPath)
    const script = join(directory, 'absolute-native-cli.mjs')
    writeFileSync(script, `#!${interpreter}\nNative script body.\n`, { mode: 0o700 })
    const capture = createKimiCatalogCapture(join(directory, 'absolute-wrapper'), { binaryName: 'kimi', executable: script })
    expect(capture.nativeIsScript).toBe(true)
    expect(capture.runtimeExecutable).toBe(interpreter)
    expect(capture.executable).toBe(realpathSync(script))
  })

  it('resolves an env Node header through the existing private environment', () => {
    const environment = { PATH: process.env.PATH, HOME: directory }
    const interpreter = findBinary('node', environment)
    if (!interpreter)
      throw new Error('The controlled header test requires the installed Node executable.')
    const script = join(directory, 'env-native-cli.mjs')
    writeFileSync(script, '#!/usr/bin/env node\nNative script body.\n', { mode: 0o700 })
    const capture = createKimiCatalogCapture(join(directory, 'env-wrapper'), { binaryName: 'kimi', executable: script }, environment)
    expect(capture.nativeIsScript).toBe(true)
    expect(capture.runtimeExecutable).toBe(realpathSync(interpreter))
  })

  it.each(['#!/usr/bin/env python', '#!node', '#!./node', '#!/absolute/unsupported-interpreter'])('rejects an unsupported or relative script interpreter: %s', (header) => {
    const script = join(directory, 'unsupported-native-cli.mjs')
    writeFileSync(script, `${header}\nNative script body.\n`, { mode: 0o700 })
    expect(() => createKimiCatalogCapture(join(directory, 'unsupported-wrapper'), { binaryName: 'kimi', executable: script })).toThrow('unsupported or relative')
  })

  it('fails when an absolute Node interpreter does not exist', () => {
    const script = join(directory, 'missing-interpreter-native-cli.mjs')
    writeFileSync(script, `#!${join(directory, 'missing', 'node')}\nNative script body.\n`, { mode: 0o700 })
    expect(() => createKimiCatalogCapture(join(directory, 'missing-interpreter-wrapper'), { binaryName: 'kimi', executable: script })).toThrow()
  })

  it('forwards actual input and split UTF-8 output and writes a private atomic native receipt', async () => {
    const current = fixture(`
process.stdin.once('data', data => {
  process.stderr.write('native diagnostics\\n');
  const ready = Buffer.from('Kimi server: http://127.0.0.1:43125/#token=native_한글\\n');
  const split = ready.indexOf(Buffer.from('한글')) + 1;
  process.stdout.write(ready.subarray(0, split));
  setImmediate(() => {
    process.stdout.write(ready.subarray(split));
    process.stdout.write('ACTUAL_INPUT:' + data.toString('utf8'));
    process.stdin.pause();
    process.exitCode = 7;
  });
});
`)
    const closed = once(current.child, 'close')
    current.child.stdin.write('actual-input-0-false')
    const [code] = await closed
    expect(code).toBe(7)
    expect(current.output()).toBe('Kimi server: http://127.0.0.1:43125/#token=native_한글\nACTUAL_INPUT:actual-input-0-false')
    expect(current.errors()).toBe('native diagnostics\n')
    const actual = parseKimiCatalogReceipt(JSON.parse(readFileSync(current.capture.receiptPath, 'utf8')), current.capture)
    expect(actual.workingDir).toBe(directory)
    expect(actual.home).toBe(directory)
    expect(actual.wrapperPid).toBe(current.child.pid)
    expect(actual.nativePid).not.toBe(current.child.pid)
    expect(actual.token).toBe('native_한글')
    expect(actual.argv).toEqual([...current.capture.launchArgs, ...runtimeArgs])
    if (process.platform !== 'win32')
      expect(statSync(current.capture.receiptPath).mode & 0o777).toBe(0o600)
  })

  it('rejects invalid native ready bytes without exposing an opaque token in diagnostics', async () => {
    const current = fixture(`process.stdout.write(Buffer.concat([Buffer.from('Kimi server: http://127.0.0.1:43125/#token=private-native-token'), Buffer.from([0xc3, 0x28]), Buffer.from('\\n')])); process.stdin.resume();`)
    const [code] = await once(current.child, 'close')
    expect(code).toBe(125)
    expect(existsSync(current.capture.receiptPath)).toBe(false)
    expect(current.errors()).toContain('could not capture a valid native ready line')
    expect(current.errors()).not.toContain('private-native-token')
  })

  it('forwards termination to the actual native process and leaves no live child', async () => {
    const current = fixture(`process.stdout.write('Kimi server: http://127.0.0.1:43125/#token=opaque-native-token\\n'); process.stdin.resume();`)
    await once(current.child.stdout, 'data')
    const actual = parseKimiCatalogReceipt(JSON.parse(readFileSync(current.capture.receiptPath, 'utf8')), current.capture)
    await stopProcess(current.child)
    expect(() => process.kill(actual.nativePid, 0)).toThrow()
    expect(current.child.exitCode !== null || current.child.signalCode !== null).toBe(true)
  })

  it('returns the actual spawn failure and closes input when the captured executable disappears', async () => {
    const executable = join(directory, 'removed-native-executable')
    writeFileSync(executable, `#!${realpathSync(process.execPath)}\n`, { mode: 0o700 })
    const capture = createKimiCatalogCapture(join(directory, 'missing-wrapper'), { binaryName: 'kimi', executable })
    rmSync(executable)
    const child = spawn(process.execPath, [capture.scriptPath, ...runtimeArgs], { cwd: directory, env: { PATH: process.env.PATH, HOME: directory }, stdio: ['pipe', 'pipe', 'pipe'] })
    children.push(child)
    let diagnostics = ''
    child.stderr.on('data', data => diagnostics += data.toString('utf8'))
    const [code] = await once(child, 'close')
    expect(code).toBe(127)
    expect(diagnostics).toContain('could not start the native executable')
    expect(existsSync(capture.receiptPath)).toBe(false)
    expect(child.stdin.destroyed).toBe(true)
  })

  it('removes a partial bearer receipt when its atomic rename fails', async () => {
    const current = fixture(`process.stdout.write('Kimi server: http://127.0.0.1:43125/#token=private-native-token\\n'); process.stdin.resume();`, capture => mkdirSync(capture.receiptPath))
    const [code] = await once(current.child, 'close')
    expect(code).toBe(125)
    expect(current.errors()).not.toContain('private-native-token')
    expect(existsSync(`${current.capture.receiptPath}.${current.child.pid}.partial`)).toBe(false)
  })
})

async function catalogServer(payload: unknown, statusCode = 200) {
  const requests: Array<{ path: string, authorization: string | undefined }> = []
  const server = createServer((request, response) => {
    requests.push({ path: request.url ?? '', authorization: request.headers.authorization })
    response.writeHead(statusCode, { 'Content-Type': 'application/json' })
    response.end(JSON.stringify(payload))
  })
  servers.push(server)
  await new Promise<void>((resolve, reject) => {
    server.once('error', reject)
    server.listen(0, '127.0.0.1', () => {
      server.off('error', reject)
      resolve()
    })
  })
  const address = server.address()
  if (!address || typeof address === 'string')
    throw new Error('The controlled Kimi catalog server has no loopback address.')
  return { origin: `http://127.0.0.1:${address.port}`, requests }
}

describe('queryKimiCompleteCatalog', () => {
  it('sends the exact native session query and bearer header to the actual loopback route', async () => {
    const native = await catalogServer(envelope([tool(), tool({ name: 'Read', active: false })]))
    await queryKimiCompleteCatalog({ origin: native.origin, token: 'opaque-native-token', sessionId: 'actual/session+한글' })
    expect(native.requests).toHaveLength(1)
    const requested = new URL(native.requests[0]!.path, native.origin)
    expect(requested.pathname).toBe('/api/v1/tools')
    expect([...requested.searchParams]).toEqual([['session_id', 'actual/session+한글']])
    expect(native.requests[0]!.authorization).toBe('Bearer opaque-native-token')
  })

  it('reads a large complete inventory without dropping inactive entries', async () => {
    const native = await catalogServer(envelope(Array.from({ length: 1000 }, (_, index) => tool({ name: `native-${index}`, active: index % 2 === 0 }))))
    const catalog = await queryKimiCompleteCatalog({ origin: native.origin, token: 'opaque-native-token', sessionId: '0' })
    expect(catalog).toHaveLength(1000)
    expect(catalog.at(-1)).toMatchObject({ name: 'native-999', active: false, input_schema: null })
  })

  it('keeps concurrent native session queries separate', async () => {
    const native = await catalogServer(envelope())
    await Promise.all(['first-native', 'second-native'].map(sessionId => queryKimiCompleteCatalog({ origin: native.origin, token: 'opaque-native-token', sessionId })))
    expect(native.requests.map(request => new URL(request.path, native.origin).searchParams.get('session_id')).sort()).toEqual(['first-native', 'second-native'])
  })

  it('rejects an absent native session before any request', async () => {
    const native = await catalogServer(envelope())
    await expect(queryKimiCompleteCatalog({ origin: native.origin, token: 'opaque-native-token', sessionId: '' })).rejects.toThrow('exact session')
    expect(native.requests).toEqual([])
  })

  it('rejects native HTTP and envelope errors without exposing its bearer token', async () => {
    const failedHTTP = await catalogServer(envelope(), 503)
    await expect(queryKimiCompleteCatalog({ origin: failedHTTP.origin, token: 'opaque-native-token', sessionId: 'native-session' })).rejects.toThrow('HTTP 503')
    const failedEnvelope = await catalogServer({ ...envelope(), code: 7 })
    await expect(queryKimiCompleteCatalog({ origin: failedEnvelope.origin, token: 'opaque-native-token', sessionId: 'native-session' })).rejects.toThrow('successful complete registry')
  })
})
