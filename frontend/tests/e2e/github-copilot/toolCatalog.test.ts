import type { ChildProcessWithoutNullStreams } from 'node:child_process'
import type { CopilotCatalogLaunch, CopilotCatalogRuntime } from './toolCatalog'
import { Buffer } from 'node:buffer'
import { spawn } from 'node:child_process'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { stopProcess } from '../helpers/process'
import { isAlive } from '../helpers/processTree'
import { waitForFileSignal } from '../helpers/toolOutputControl'
import { CopilotCatalogFrames, copilotCatalogNames, queryCopilotBuiltinCatalog } from './toolCatalog'

function packet(value: unknown) {
  const body = Buffer.from(JSON.stringify(value))
  return Buffer.concat([Buffer.from(`Content-Length: ${body.byteLength}\r\n\r\n`), body])
}

describe('CopilotCatalogFrames', () => {
  it('retains UTF-8 bytes across every split position', () => {
    const value = { id: 1, result: { tools: [{ name: 'native_출력' }] } }
    const bytes = packet(value)
    for (let index = 0; index <= bytes.length; index++) {
      const frames = new CopilotCatalogFrames()
      expect([...frames.push(bytes.subarray(0, index)), ...frames.push(bytes.subarray(index))]).toEqual([value])
    }
  })

  it('decodes multiple replies and keeps a trailing partial packet', () => {
    const first = packet({ id: 1, result: { tools: [{ name: 'read' }] } })
    const second = packet({ id: 2, result: {} })
    const frames = new CopilotCatalogFrames()
    expect(frames.push(Buffer.concat([first, second.subarray(0, 5)]))).toHaveLength(1)
    expect(frames.push(second.subarray(5))).toEqual([{ id: 2, result: {} }])
  })

  it.each(['Content-Length: 0\r\n\r\n', 'Content-Length: 16777217\r\n\r\n', 'Content-Length: nope\r\n\r\n'])('refuses an invalid native frame header: %s', (header) => {
    expect(() => new CopilotCatalogFrames().push(Buffer.from(header))).toThrow('invalid length')
  })

  it('refuses duplicate Content-Length headers before accepting a reply', () => {
    const body = Buffer.from(JSON.stringify({ jsonrpc: '2.0', id: 1, result: { tools: [{ name: 'read' }] } }))
    expect(() => new CopilotCatalogFrames().push(Buffer.concat([Buffer.from(`Content-Length: ${body.byteLength}\r\nContent-Length: ${body.byteLength}\r\n\r\n`), body]))).toThrow('header')
  })

  it('rejects an oversized incomplete header before accumulating a body', () => {
    expect(() => new CopilotCatalogFrames().push(Buffer.from('x'.repeat(8193)))).toThrow('header is too large')
  })

  it('rejects invalid UTF-8 rather than replacing bytes in a native tool name', () => {
    const body = Buffer.concat([Buffer.from('{"result":{"tools":[{"name":"'), Buffer.from([0xC3, 0x28]), Buffer.from('"}]}}')])
    expect(() => new CopilotCatalogFrames().push(Buffer.concat([Buffer.from(`Content-Length: ${body.byteLength}\r\n\r\n`), body]))).toThrow()
  })

  it('requires complete bytes when a stream ends and accepts an empty completed buffer', () => {
    const frames = new CopilotCatalogFrames()
    frames.push(packet({ id: 1, result: {} }))
    expect(() => frames.finish()).not.toThrow()
    frames.push(Buffer.from('Content-Length: 100\r\n\r\n{'))
    expect(() => frames.finish()).toThrow('incomplete frame')
  })
})

describe('copilotCatalogNames', () => {
  it('keeps every complete builtin name', () => {
    expect(copilotCatalogNames({ tools: [{ name: 'read' }, { name: 'bash', deferLoading: true }] })).toEqual(['read', 'bash'])
  })

  it.each([null, {}, { tools: [] }, { tools: [{}] }, { tools: [{ name: 'read' }, { name: 'read' }] }])('refuses an incomplete or ambiguous native inventory: %j', (value) => {
    expect(() => copilotCatalogNames(value)).toThrow()
  })
})

let directory: string
const children: ChildProcessWithoutNullStreams[] = []

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  directory = mkdtempSync(join(scratch, 'copilot-catalog-transport-'))
})

afterEach(async () => {
  await Promise.all(children.splice(0).map(child => stopProcess(child)))
  rmSync(directory, { recursive: true, force: true })
})

function nativeFixture(responseSource: string, options: { model?: string } = {}) {
  const script = join(directory, 'native-server.cjs')
  const receipt = join(directory, 'request.json')
  writeFileSync(script, `
const fs = require('node:fs');
let buffered = Buffer.alloc(0);
const packet = value => { const body = Buffer.from(JSON.stringify(value)); return Buffer.concat([Buffer.from('Content-Length: ' + body.byteLength + '\\r\\n\\r\\n'), body]); };
process.stdin.on('data', chunk => {
  buffered = Buffer.concat([buffered, chunk]);
  const end = buffered.indexOf('\\r\\n\\r\\n');
  if (end < 0) return;
  const length = Number(/Content-Length: (\\d+)/.exec(buffered.subarray(0, end).toString('ascii'))[1]);
  if (buffered.length < end + 4 + length) return;
  const request = JSON.parse(buffered.subarray(end + 4, end + 4 + length).toString('utf8'));
  fs.writeFileSync(${JSON.stringify(receipt)}, JSON.stringify({ request, pid: process.pid, workingDir: process.cwd(), privateValue: process.env.COPILOT_CATALOG_PRIVATE_VALUE }));
  ${responseSource}
});
`)
  const launch: CopilotCatalogLaunch = {
    executable: process.execPath,
    args: [script],
    cwd: directory,
    env: { PATH: process.env.PATH, COPILOT_CATALOG_PRIVATE_VALUE: 'private-zero-0-false-한글' },
    ...options.model !== undefined ? { model: options.model } : {},
  }
  const start = (selected: CopilotCatalogLaunch) => {
    const child = spawn(selected.executable, [...selected.args], { cwd: selected.cwd, env: selected.env, stdio: ['pipe', 'pipe', 'pipe'] })
    children.push(child)
    return child
  }
  return { launch, receipt, start }
}

function queryFixture(responseSource: string, runtime: CopilotCatalogRuntime = {}, options: { model?: string } = {}) {
  const fixture = nativeFixture(responseSource, options)
  const query = queryCopilotBuiltinCatalog(fixture.launch, { start: fixture.start, ...runtime })
  return { ...fixture, query }
}

const success = 'process.stdout.write(packet({ jsonrpc: \'2.0\', id: request.id, result: { tools: [{ name: \'read\' }, { name: \'native_출력\' }] } }));'

describe('queryCopilotBuiltinCatalog', () => {
  it('sends the exact method, model, directory, and private environment and stops its actual process', async () => {
    const fixture = queryFixture(`
const reply = packet({ jsonrpc: '2.0', id: request.id, result: { tools: [{ name: 'read' }, { name: 'native_출력' }] } });
const split = reply.indexOf(Buffer.from('출력')) + 1;
process.stdout.write(reply.subarray(0, 7));
setImmediate(() => { process.stdout.write(reply.subarray(7, split)); setImmediate(() => process.stdout.write(reply.subarray(split))); });
`, {}, { model: 'actual-model-한글' })
    await expect(fixture.query).resolves.toEqual(['read', 'native_출력'])
    const receipt = JSON.parse(readFileSync(fixture.receipt, 'utf8'))
    expect(receipt.request).toEqual({ jsonrpc: '2.0', id: 1, method: 'tools.list', params: { model: 'actual-model-한글' } })
    expect(receipt.workingDir).toBe(directory)
    expect(receipt.privateValue).toBe('private-zero-0-false-한글')
    expect(isAlive(receipt.pid)).toBe(false)
  })

  it('omits an absent model and ignores unrelated valid replies', async () => {
    const fixture = queryFixture(`process.stdout.write(Buffer.concat([packet({ jsonrpc: '2.0', id: 99, result: { tools: [{ name: 'wrong' }] } }), packet({ jsonrpc: '2.0', id: request.id, result: { tools: [{ name: 'actual' }] } })]));`)
    await expect(fixture.query).resolves.toEqual(['actual'])
    expect(JSON.parse(readFileSync(fixture.receipt, 'utf8')).request.params).toEqual({})
  })

  it('preserves an actual native error code and message', async () => {
    await expect(queryFixture('process.stdout.write(packet({ jsonrpc: \'2.0\', id: request.id, error: { code: -32601, message: \'Native catalog unsupported 한글\' } }));').query).rejects.toThrow('-32601')
  })

  it('retains the actual early exit code and stops its process', async () => {
    const fixture = queryFixture('process.exit(7);')
    await expect(fixture.query).rejects.toThrow('code 7')
    const receipt = JSON.parse(readFileSync(fixture.receipt, 'utf8'))
    expect(isAlive(receipt.pid)).toBe(false)
  })

  it.each([
    { label: 'wrong version', reply: { jsonrpc: '1.0', id: 1, result: { tools: [{ name: 'incorrect-success' }] } } },
    { label: 'method in reply', reply: { jsonrpc: '2.0', id: 1, method: 'tools.list', result: { tools: [{ name: 'incorrect-success' }] } } },
    { label: 'both result and null error', reply: { jsonrpc: '2.0', id: 1, result: { tools: [{ name: 'incorrect-success' }] }, error: null } },
    { label: 'an invalid null ID', reply: { jsonrpc: '2.0', id: null, result: { tools: [{ name: 'incorrect-success' }] } } },
    { label: 'a malformed error', reply: { jsonrpc: '2.0', id: 1, error: { code: -32603, message: 0 } } },
  ])('refuses a matching native envelope with $label', async ({ reply }) => {
    await expect(queryFixture(`process.stdout.write(packet(${JSON.stringify(reply)}));`).query).rejects.toThrow('envelope')
  })

  it('rejects a matching native reply with no result', async () => {
    await expect(queryFixture('process.stdout.write(packet({ jsonrpc: \'2.0\', id: request.id }));').query).rejects.toThrow()
  })

  it('rejects duplicate matching success replies in one actual stdout write', async () => {
    await expect(queryFixture('const reply = packet({ jsonrpc: \'2.0\', id: request.id, result: { tools: [{ name: \'read\' }] } }); process.stdout.write(Buffer.concat([reply, reply]));').query).rejects.toThrow('repeated')
  })

  it('rejects success followed by a native error in the same actual stdout write', async () => {
    await expect(queryFixture('process.stdout.write(Buffer.concat([packet({ jsonrpc: \'2.0\', id: request.id, result: { tools: [{ name: \'read\' }] } }), packet({ jsonrpc: \'2.0\', id: request.id, error: { code: -32603, message: \'Native trailing failure\' } })]));').query).rejects.toThrow('Native trailing failure')
  })

  it('rejects malformed JSON instead of accepting native output', async () => {
    await expect(queryFixture('const body = Buffer.from(\'{broken\'); process.stdout.write(Buffer.concat([Buffer.from(\'Content-Length: \' + body.length + \'\\r\\n\\r\\n\'), body]));').query).rejects.toThrow()
  })

  it('rejects valid success followed by malformed bytes in the same actual stdout write', async () => {
    await expect(queryFixture('process.stdout.write(Buffer.concat([packet({ jsonrpc: \'2.0\', id: request.id, result: { tools: [{ name: \'read\' }] } }), Buffer.from(\'Content-Length: nope\\r\\n\\r\\n\')]));').query).rejects.toThrow('invalid length')
  })

  it('rejects natural stdout EOF with an incomplete frame before a reply', async () => {
    await expect(queryFixture('process.stdout.end(\'Content-Length: 100\\r\\n\\r\\n{\'); process.stdin.pause();').query).rejects.toThrow('incomplete')
  })

  it('rejects empty native stdout EOF before a reply without waiting for its deadline', async () => {
    const fixture = nativeFixture('process.stdout.end();')
    let deadline: (() => void) | undefined
    let resolveEnded!: () => void
    const ended = new Promise<void>((resolve) => {
      resolveEnded = resolve
    })
    const query = queryCopilotBuiltinCatalog(fixture.launch, {
      start: (launch) => {
        const child = fixture.start(launch)
        child.stdout.once('end', resolveEnded)
        return child
      },
      scheduleDeadline: (fail) => {
        deadline = fail
        return () => {}
      },
    })
    const assertion = expect(query).rejects.toThrow('ended without a catalog reply')
    await ended
    if (!deadline)
      throw new Error('The empty-EOF regression contains no request deadline.')
    setImmediate(deadline)
    await assertion
  })

  it('preserves cleanup failure after native success', async () => {
    const failure = new Error('Controlled native cleanup failure')
    const fixture = queryFixture(success, {
      stop: async (child) => {
        await stopProcess(child)
        throw failure
      },
    })
    await expect(fixture.query).rejects.toBe(failure)
  })

  it('preserves native request and cleanup failures as separate causes', async () => {
    const failure = new Error('Controlled native cleanup failure')
    const fixture = queryFixture('process.stdout.write(packet({ jsonrpc: \'2.0\', id: request.id, error: { code: -32603, message: \'Controlled native request failure\' } }));', {
      stop: async (child) => {
        await stopProcess(child)
        throw failure
      },
    })
    const rejected = await fixture.query.catch(error => error)
    expect(rejected).toBeInstanceOf(AggregateError)
    if (!(rejected instanceof AggregateError))
      throw new Error('The catalog query lost its aggregate failure.')
    expect(rejected.errors).toHaveLength(2)
    expect(rejected.errors[0]).toBeInstanceOf(Error)
    expect(rejected.errors[0].message).toContain('Controlled native request failure')
    expect(rejected.errors[1]).toBe(failure)
  })

  it('preserves an actual stdin write failure and stops the child', async () => {
    const fixture = nativeFixture(success)
    const query = queryCopilotBuiltinCatalog(fixture.launch, {
      start: (launch) => {
        const child = fixture.start(launch)
        child.stdin.destroy()
        return child
      },
    })
    await expect(query).rejects.toMatchObject({ code: 'ERR_STREAM_DESTROYED' })
    expect(children.at(-1)?.exitCode !== null || children.at(-1)?.signalCode !== null).toBe(true)
  })

  it('rejects an absent executable and cancels its deadline', async () => {
    const fixture = nativeFixture(success)
    const cancel = vi.fn()
    await expect(queryCopilotBuiltinCatalog({ ...fixture.launch, executable: join(directory, 'absent-executable') }, { start: fixture.start, scheduleDeadline: () => cancel })).rejects.toMatchObject({ code: 'ENOENT' })
    expect(cancel).toHaveBeenCalledOnce()
  })

  it('uses a controlled deadline only after the actual native request and cancels it after failure', async () => {
    let deadline: (() => void) | undefined
    const cancel = vi.fn()
    const fixture = queryFixture('', {
      scheduleDeadline: (fail) => {
        deadline = fail
        return cancel
      },
    })
    const assertion = expect(fixture.query).rejects.toThrow('request deadline')
    await waitForFileSignal(fixture.receipt)
    if (!deadline)
      throw new Error('The query did not register its request deadline.')
    deadline()
    await assertion
    expect(cancel).toHaveBeenCalledOnce()
    expect(isAlive(JSON.parse(readFileSync(fixture.receipt, 'utf8')).pid)).toBe(false)
  })

  it('cancels the deadline after a successful actual native reply', async () => {
    const cancel = vi.fn()
    await expect(queryFixture(success, { scheduleDeadline: () => cancel }).query).resolves.toEqual(['read', 'native_출력'])
    expect(cancel).toHaveBeenCalledOnce()
  })

  it('removes its transport listeners before intentional process cleanup', async () => {
    const fixture = nativeFixture(success)
    let childBeforeQuery: ChildProcessWithoutNullStreams | undefined
    let dataListeners: unknown[] = []
    let endListeners: unknown[] = []
    let errorListeners: unknown[] = []
    const query = queryCopilotBuiltinCatalog(fixture.launch, {
      start: (launch) => {
        const child = fixture.start(launch)
        childBeforeQuery = child
        dataListeners = child.stdout.listeners('data')
        endListeners = child.stdout.listeners('end')
        errorListeners = child.stdin.listeners('error')
        return child
      },
      stop: async (child) => {
        expect(child).toBe(childBeforeQuery)
        expect(child.stdout.listeners('data')).toEqual(dataListeners)
        expect(child.stdout.listeners('end')).toEqual(endListeners)
        expect(child.stdin.listeners('error')).toEqual(errorListeners)
        await stopProcess(child)
      },
    })
    await expect(query).resolves.toEqual(['read', 'native_출력'])
  })
})
