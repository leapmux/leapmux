import type { CommandProcess } from '../../../scripts/e2eCommandProcess'
import { spawnCommandProcess } from '../../../scripts/e2eCommandProcess'
import { isObject } from '../../../src/lib/jsonPick'
import { cleanupOnFailure } from '../helpers/cleanup'
import { createProcessOutputLineDecoder } from '../helpers/processOutputLines'
import { trackProcess } from '../helpers/processRegistry'

export interface MuseNativeLaunch {
  executable: string
  args: readonly string[]
  cwd: string
  env: NodeJS.ProcessEnv
  runDirectory: string
}

export interface MuseNativeFrame {
  readonly raw: string
  readonly value: Record<string, unknown>
}

export interface MuseNativeRuntime {
  start?: (launch: MuseNativeLaunch) => CommandProcess
  scheduleDeadline?: (fail: () => void) => (() => void)
}

interface PendingRequest {
  resolve: (value: Record<string, unknown>) => void
  reject: (error: Error) => void
  cancel: () => void
}

interface NotificationWait {
  matches: (frame: MuseNativeFrame) => boolean
  resolve: (frame: MuseNativeFrame) => void
  reject: (error: Error) => void
  cancel: () => void
}

/** Own one Muse protocol stream and its exact native process tree. */
export class MuseNativeHost {
  readonly frames: MuseNativeFrame[] = []
  readonly stderr: string[] = []
  private readonly owner: CommandProcess
  private readonly pending = new Map<number, PendingRequest>()
  private readonly replied = new Set<number>()
  private readonly waits = new Set<NotificationWait>()
  private readonly detach: () => void
  private nextId = 0
  private ended = false
  private failure: Error | undefined
  private closing: Promise<void> | undefined

  static async start(launch: MuseNativeLaunch, runtime: MuseNativeRuntime = {}): Promise<MuseNativeHost> {
    const owner = runtime.start?.(launch) ?? spawnCommandProcess(launch.executable, [...launch.args], {
      cwd: launch.cwd,
      env: launch.env,
      stdio: ['pipe', 'pipe', 'pipe'],
    }, { ownTree: true })
    let host: MuseNativeHost | undefined
    return cleanupOnFailure(async () => {
      host = new MuseNativeHost(owner, runtime)
      trackProcess(launch.runDirectory, owner.child)
      return host
    }, () => host?.close() ?? owner.stop())
  }

  private constructor(owner: CommandProcess, private readonly runtime: MuseNativeRuntime) {
    this.owner = owner
    const child = this.owner.child
    if (!child.stdin || !child.stdout || !child.stderr)
      throw new Error('The native Muse host requires three owned pipe streams.')
    const decoder = createProcessOutputLineDecoder((raw) => {
      try {
        this.acceptFrame(raw)
      }
      catch (error) {
        this.fail(error)
      }
    })
    const onData = (chunk: Uint8Array) => decoder.write(chunk)
    const onEnd = () => {
      decoder.end()
      this.ended = true
      this.fail(new Error('The native Muse output stream ended before its pending reply or notification.'))
    }
    const onError = (error: Error) => this.fail(error)
    const onClose = (code: number | null, signal: NodeJS.Signals | null) => {
      this.ended = true
      this.fail(new Error(`The native Muse process exited: code ${code}, signal ${signal}.`))
      this.detach()
    }
    const stderrDecoder = new TextDecoder()
    const onStderr = (chunk: Uint8Array) => this.stderr.push(stderrDecoder.decode(chunk, { stream: true }))
    child.stdout.on('data', onData)
    child.stdout.once('end', onEnd)
    child.stdout.once('error', onError)
    child.stdin.once('error', onError)
    child.stderr.on('data', onStderr)
    child.stderr.once('error', onError)
    child.once('error', onError)
    child.once('close', onClose)
    this.detach = () => {
      child.stdout?.off('data', onData)
      child.stdout?.off('end', onEnd)
      child.stdout?.off('error', onError)
      child.stdin?.off('error', onError)
      child.stderr?.off('data', onStderr)
      child.stderr?.off('error', onError)
      child.off('error', onError)
      child.off('close', onClose)
    }
  }

  private deadline(fail: () => void): () => void {
    if (this.runtime.scheduleDeadline)
      return this.runtime.scheduleDeadline(fail)
    const timer = setTimeout(fail, 30_000)
    return () => clearTimeout(timer)
  }

  private fail(cause: unknown): void {
    this.failure ??= cause instanceof Error ? cause : new Error(String(cause))
    for (const request of this.pending.values()) {
      request.cancel()
      request.reject(this.failure)
    }
    this.pending.clear()
    for (const wait of this.waits) {
      wait.cancel()
      wait.reject(this.failure)
    }
    this.waits.clear()
  }

  private acceptFrame(raw: string): void {
    if (this.failure)
      return
    const value: unknown = JSON.parse(raw)
    if (!isObject(value) || value.jsonrpc !== '2.0')
      throw new Error('The native Muse frame has an invalid JSON-RPC envelope.')
    const result = Object.hasOwn(value, 'result')
    const error = Object.hasOwn(value, 'error')
    const method = Object.hasOwn(value, 'method')
    if (value.id !== undefined && typeof value.id !== 'string' && !(typeof value.id === 'number' && Number.isSafeInteger(value.id)))
      throw new Error('The native Muse frame has an invalid request ID.')
    if (method) {
      if (typeof value.method !== 'string' || !value.method || result || error || !isObject(value.params))
        throw new Error('The native Muse frame has an invalid request or notification.')
    }
    else if (value.id === undefined || result === error || (result && !isObject(value.result))
      || (error && (!isObject(value.error) || typeof value.error.code !== 'number' || !Number.isSafeInteger(value.error.code) || typeof value.error.message !== 'string'))) {
      throw new Error('The native Muse frame has an invalid reply.')
    }
    const frame = { raw, value }
    this.frames.push(frame)
    if (!method && typeof value.id === 'number') {
      if (this.replied.has(value.id))
        throw new Error('The native Muse host repeats a local reply ID.')
      const request = this.pending.get(value.id)
      if (request) {
        this.pending.delete(value.id)
        this.replied.add(value.id)
        request.cancel()
        if (error)
          request.reject(new Error(`The native Muse request failed: ${JSON.stringify(value.error)}`))
        else
          request.resolve(value.result as Record<string, unknown>)
      }
    }
    for (const wait of this.waits) {
      if (method && wait.matches(frame)) {
        this.waits.delete(wait)
        wait.cancel()
        wait.resolve(frame)
      }
    }
  }

  private write(value: Record<string, unknown>): void {
    if (this.failure)
      throw this.failure
    if (this.ended || this.closing)
      throw new Error('The native Muse host already ended.')
    this.owner.child.stdin!.write(`${JSON.stringify(value)}\n`, (error) => {
      if (error)
        this.fail(error)
    })
  }

  request(method: string, params: Record<string, unknown>): Promise<Record<string, unknown>> {
    if (this.failure || this.ended || this.closing)
      return Promise.reject(this.failure ?? new Error('The native Muse host already ended.'))
    if (!method || !isObject(params))
      return Promise.reject(new Error('The native Muse request requires a method and object params.'))
    const id = ++this.nextId
    return new Promise((resolve, reject) => {
      const cancel = this.deadline(() => this.fail(new Error(`The native Muse request ${method} exceeded its deadline.`)))
      this.pending.set(id, { resolve, reject, cancel })
      try {
        this.write({ jsonrpc: '2.0', id, method, params })
      }
      catch (error) {
        this.fail(error)
      }
    })
  }

  notify(method: string, params: Record<string, unknown> = {}): void {
    if (!method || !isObject(params))
      throw new Error('The native Muse notification requires a method and object params.')
    this.write({ jsonrpc: '2.0', method, params })
  }

  waitForNotification(matches: (frame: MuseNativeFrame) => boolean): Promise<MuseNativeFrame> {
    if (this.failure)
      return Promise.reject(this.failure)
    const observed = this.frames.find(frame => typeof frame.value.method === 'string' && matches(frame))
    if (observed)
      return Promise.resolve(observed)
    if (this.failure || this.ended || this.closing)
      return Promise.reject(this.failure ?? new Error('The native Muse host already ended.'))
    return new Promise((resolve, reject) => {
      const cancel = this.deadline(() => this.fail(new Error('The native Muse notification exceeded its deadline.')))
      this.waits.add({ matches, resolve, reject, cancel })
    })
  }

  close(): Promise<void> {
    this.closing ??= (async () => {
      this.fail(new Error('The native Muse host closed.'))
      try {
        await this.owner.stop()
      }
      finally {
        this.detach()
      }
    })()
    return this.closing
  }
}
