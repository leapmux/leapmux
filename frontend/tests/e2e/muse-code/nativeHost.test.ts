import type { ChildProcess } from 'node:child_process'
import type { MuseNativeLaunch } from './nativeHost'
import { EventEmitter } from 'node:events'
import { existsSync, mkdirSync, mkdtempSync, readdirSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { PassThrough, Writable } from 'node:stream'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { MuseNativeHost } from './nativeHost'

let directory: string
beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  directory = mkdtempSync(join(scratch, 'muse-native-host-test-'))
})
afterEach(() => rmSync(directory, { recursive: true, force: true }))

async function controlledHost(writeFailure?: Error) {
  const child = Object.assign(new EventEmitter(), {
    stdin: writeFailure ? new Writable({ write: (_chunk, _encoding, callback) => callback(writeFailure) }) : new PassThrough(),
    stdout: new PassThrough(),
    stderr: new PassThrough(),
    exitCode: null as number | null,
    signalCode: null as NodeJS.Signals | null,
    pid: undefined,
  })
  const cancellations: ReturnType<typeof vi.fn>[] = []
  const deadlines: (() => void)[] = []
  const stop = vi.fn(async () => {})
  const launch: MuseNativeLaunch = { executable: '/private/muse', args: ['serve'], cwd: directory, env: {}, runDirectory: directory }
  const host = await MuseNativeHost.start(launch, {
    start: () => ({ child: child as unknown as ChildProcess, stop }),
    scheduleDeadline: (fail) => {
      deadlines.push(fail)
      const cancel = vi.fn()
      cancellations.push(cancel)
      return cancel
    },
  })
  return { host, child, stop, deadlines, cancellations }
}

describe('MuseNativeHost', () => {
  it('owns a real subprocess and removes its private process record after a normal reply', async () => {
    const script = `const { createInterface } = require('node:readline');
createInterface({ input: process.stdin }).on('line', raw => {
  const frame = JSON.parse(raw);
  process.stdout.write(JSON.stringify({ jsonrpc: '2.0', id: frame.id, result: { method: frame.method, params: frame.params, marker: '\u754C' } }) + '\\n');
});`
    const host = await MuseNativeHost.start({ executable: process.execPath, args: ['--eval', script], cwd: directory, env: {}, runDirectory: directory })
    try {
      const result = await host.request('initialize', { zero: 0, empty: '' })
      expect(result).toEqual({ method: 'initialize', params: { zero: 0, empty: '' }, marker: '\u754C' })
      expect(readdirSync(join(directory, 'processes'))).toHaveLength(1)
    }
    finally {
      await host.close()
    }
    expect(readdirSync(join(directory, 'processes'))).toEqual([])
  })

  it('rejects a real process exit before its reply and closes after that exit', async () => {
    const host = await MuseNativeHost.start({ executable: process.execPath, args: ['--eval', 'process.exit(7)'], cwd: directory, env: {}, runDirectory: directory })
    try {
      await expect(host.request('initialize', {})).rejects.toThrow(/ended|exited/)
    }
    finally {
      await host.close()
    }
    expect(existsSync(join(directory, 'processes')) ? readdirSync(join(directory, 'processes')) : []).toEqual([])
  })

  it('releases an owned process when its initialization fails and retains a cleanup failure', async () => {
    const failure = new Error('owned process cleanup failed')
    const child = Object.assign(new EventEmitter(), { stdin: null, stdout: null, stderr: null })
    const stop = vi.fn(async () => {
      throw failure
    })
    const launch: MuseNativeLaunch = { executable: '/private/muse', args: ['serve'], cwd: directory, env: {}, runDirectory: directory }
    const result = await MuseNativeHost.start(launch, { start: () => ({ child: child as unknown as ChildProcess, stop }) }).catch(error => error)
    expect(result).toBeInstanceOf(AggregateError)
    expect(result.errors[0].message).toContain('three owned pipe streams')
    expect(result.errors[1]).toBe(failure)
    expect(stop).toHaveBeenCalledOnce()
  })

  it('rejects pending requests and notification waits when the native process exits', async () => {
    const { host, child, cancellations } = await controlledHost()
    let requestFailure: unknown
    let notificationFailure: unknown
    const request = host.request('initialize', {}).catch((error) => {
      requestFailure = error
    })
    const notification = host.waitForNotification(() => false).catch((error) => {
      notificationFailure = error
    })
    child.exitCode = 2
    child.emit('close', 2, null)
    await Promise.resolve()
    try {
      expect(requestFailure).toBeInstanceOf(Error)
      expect(notificationFailure).toBeInstanceOf(Error)
      expect(String(requestFailure)).toContain('code 2')
      expect(cancellations.every(cancel => cancel.mock.calls.length === 1)).toBe(true)
    }
    finally {
      await host.close()
      await Promise.all([request, notification])
    }
  })

  it('cancels the request deadline after a native exit and closes the owned process once', async () => {
    const { host, child, stop, deadlines, cancellations } = await controlledHost()
    const request = host.request('initialize', {}).catch(error => error)
    child.exitCode = 0
    child.emit('close', 0, null)
    try {
      expect(cancellations[0]).toHaveBeenCalledOnce()
      deadlines[0]!()
      await Promise.all([host.close(), host.close()])
      expect(stop).toHaveBeenCalledOnce()
    }
    finally {
      await host.close()
      await request
    }
  })

  it('correlates only an exact local ID and preserves the original reply bytes', async () => {
    const { host, child, cancellations } = await controlledHost()
    const request = host.request('initialize', {})
    let settled = false
    void request.then(() => {
      settled = true
    })
    child.stdout.write('{"jsonrpc":"2.0","id":"1","result":{"foreign":true}}\n')
    child.stdout.write('{"jsonrpc":"2.0","id":99,"result":{}}\n')
    await Promise.resolve()
    expect(settled).toBe(false)
    const raw = '{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"muse"},"extra":0}}'
    child.stdout.write(`${raw}\n`)
    expect(await request).toEqual({ serverInfo: { name: 'muse' }, extra: 0 })
    expect(host.frames.at(-1)?.raw).toBe(raw)
    expect(cancellations[0]).toHaveBeenCalledOnce()
    await host.close()
  })

  it.each([
    'not JSON',
    '{"jsonrpc":"2.0","id":1}',
    '{"jsonrpc":"2.0","id":1,"result":null}',
    '{"jsonrpc":"2.0","id":1,"result":{},"error":{"code":1,"message":"both"}}',
    '{"jsonrpc":"2.0","id":1,"error":{"code":"invalid","message":"failure"}}',
    '{"jsonrpc":"2.0","id":1.5,"result":{}}',
  ])('rejects a malformed native reply: %s', async (raw) => {
    const { host, child } = await controlledHost()
    const reply = host.request('initialize', {})
    child.stdout.write(`${raw}\n`)
    await expect(reply).rejects.toBeInstanceOf(Error)
    await host.close()
  })

  it('rejects a duplicate local reply before it can settle another request', async () => {
    const { host, child } = await controlledHost()
    const first = host.request('initialize', {})
    const raw = '{"jsonrpc":"2.0","id":1,"result":{}}\n'
    child.stdout.write(raw)
    await first
    const second = host.request('session/start', {})
    child.stdout.write(raw)
    await expect(second).rejects.toThrow('repeats a local reply ID')
    await host.close()
  })

  it('rejects a failed stdin write and preserves its error', async () => {
    const failure = new Error('native write failed')
    const { host } = await controlledHost(failure)
    await expect(host.request('initialize', {})).rejects.toBe(failure)
    await host.close()
  })

  it('rejects a stream error and removes its listeners when the owned process closes', async () => {
    const { host, child, stop } = await controlledHost()
    const request = host.request('initialize', {})
    const failure = new Error('native stream failed')
    child.stdin.emit('error', failure)
    await expect(request).rejects.toBe(failure)
    await host.close()
    expect(stop).toHaveBeenCalledOnce()
    expect(child.listenerCount('close')).toBe(0)
    expect(child.stdout.listenerCount('data')).toBe(0)
    expect(child.stdin.listenerCount('error')).toBe(0)
  })

  it('rejects later writes after close', async () => {
    const { host } = await controlledHost()
    await host.close()
    await expect(host.request('initialize', {})).rejects.toThrow('closed')
    expect(() => host.notify('initialized')).toThrow('closed')
  })
})
