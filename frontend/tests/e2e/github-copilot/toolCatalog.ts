import type { ChildProcessWithoutNullStreams } from 'node:child_process'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { Buffer } from 'node:buffer'
import { spawn } from 'node:child_process'
import { isObject } from '../../../src/lib/jsonPick'
import { requireBinary } from '../helpers/binaryOnPath'
import { withCleanup } from '../helpers/cleanup'
import { currentNativeAgent, nativeOptionValue } from '../helpers/nativeScenario'
import { stopProcess } from '../helpers/process'
import { hubSpawnEnv } from '../helpers/server'

const MAX_CATALOG_FRAME_BYTES = 16 * 1024 * 1024
const MAX_CATALOG_HEADER_BYTES = 8 * 1024
const CATALOG_REQUEST_DEADLINE_MS = 30_000

/** Decode native Copilot Content-Length frames without losing split UTF-8 bytes. */
export class CopilotCatalogFrames {
  private buffered = Buffer.alloc(0)

  push(chunk: Uint8Array): Record<string, unknown>[] {
    this.buffered = Buffer.concat([this.buffered, chunk])
    const frames: Record<string, unknown>[] = []
    for (;;) {
      const end = this.buffered.indexOf('\r\n\r\n')
      if (end < 0) {
        if (this.buffered.length > MAX_CATALOG_HEADER_BYTES)
          throw new Error('The native Copilot catalog frame header is too large.')
        return frames
      }
      if (end > MAX_CATALOG_HEADER_BYTES)
        throw new Error('The native Copilot catalog frame header is too large.')
      const header = this.buffered.subarray(0, end).toString('ascii')
      const lengths = header.split('\r\n').filter(line => /^Content-Length:/i.test(line))
      if (lengths.length !== 1)
        throw new Error('The native Copilot catalog frame header requires one Content-Length.')
      const length = Number(/^Content-Length:[ \t]*(\d+)[ \t]*$/i.exec(lengths[0]!)?.[1])
      if (!Number.isSafeInteger(length) || length < 1 || length > MAX_CATALOG_FRAME_BYTES)
        throw new Error('The native Copilot catalog frame has an invalid length.')
      if (this.buffered.length < end + 4 + length)
        return frames
      const body = new TextDecoder('utf-8', { fatal: true }).decode(this.buffered.subarray(end + 4, end + 4 + length))
      const frame: unknown = JSON.parse(body)
      this.buffered = this.buffered.subarray(end + 4 + length)
      if (!isObject(frame))
        throw new Error('The native Copilot catalog frame has no object envelope.')
      frames.push(frame)
    }
  }

  finish(): void {
    if (this.buffered.length !== 0)
      throw new Error('The native Copilot catalog stream ended with an incomplete frame.')
  }
}

export function copilotCatalogNames(value: unknown): string[] {
  if (!isObject(value) || !Array.isArray(value.tools) || value.tools.length === 0)
    throw new Error('The complete native Copilot catalog contains no tools.')
  const names = value.tools.map((tool: unknown) => {
    if (!isObject(tool) || typeof tool.name !== 'string' || !tool.name)
      throw new Error('The complete native Copilot catalog contains an invalid tool.')
    return tool.name
  })
  if (new Set(names).size !== names.length)
    throw new Error('The complete native Copilot catalog repeats a tool.')
  return names
}

export interface CopilotCatalogLaunch {
  executable: string
  args: readonly string[]
  cwd: string
  env: NodeJS.ProcessEnv
  model?: string
}

export interface CopilotCatalogRuntime {
  start?: (launch: CopilotCatalogLaunch) => ChildProcessWithoutNullStreams
  stop?: (child: ChildProcessWithoutNullStreams) => Promise<void>
  scheduleDeadline?: (fail: () => void) => (() => void)
}

/** Validate requests, notifications, and replies before their IDs can satisfy the catalog query. */
function validateCatalogEnvelope(frame: Record<string, unknown>): void {
  if (frame.jsonrpc !== '2.0')
    throw new Error('The native Copilot catalog frame has an invalid JSON-RPC envelope.')
  const hasResult = Object.hasOwn(frame, 'result')
  const hasError = Object.hasOwn(frame, 'error')
  const hasMethod = Object.hasOwn(frame, 'method')
  if (hasMethod) {
    if (typeof frame.method !== 'string' || frame.method.length === 0 || hasResult || hasError)
      throw new Error('The native Copilot catalog frame has an invalid JSON-RPC envelope.')
  }
  else if (hasResult === hasError || frame.id === undefined) {
    throw new Error('The native Copilot catalog frame has an invalid JSON-RPC envelope.')
  }
  if (frame.id !== undefined && typeof frame.id !== 'string' && !(typeof frame.id === 'number' && Number.isSafeInteger(frame.id)))
    throw new Error('The native Copilot catalog frame has an invalid JSON-RPC envelope.')
  if (hasError && (!isObject(frame.error) || typeof frame.error.code !== 'number' || !Number.isSafeInteger(frame.error.code) || typeof frame.error.message !== 'string'))
    throw new Error('The native Copilot catalog frame has an invalid JSON-RPC envelope.')
}

/** Query one native process. Controlled tests supply real subprocess I/O through the runtime seam. */
export async function queryCopilotBuiltinCatalog(launch: CopilotCatalogLaunch, runtime: CopilotCatalogRuntime = {}): Promise<string[]> {
  const child = runtime.start?.(launch) ?? spawn(launch.executable, [...launch.args], { cwd: launch.cwd, env: launch.env, stdio: ['pipe', 'pipe', 'pipe'] })
  let endedWithoutReply: Error | undefined
  return withCleanup(async () => {
    const frames = new CopilotCatalogFrames()
    let cancelDeadline: (() => void) | undefined
    let detachListeners: (() => void) | undefined
    try {
      return await new Promise<string[]>((resolve, reject) => {
        let settled = false
        const fail = (cause: unknown) => {
          if (settled)
            return
          settled = true
          reject(cause instanceof Error ? cause : new Error(String(cause)))
        }
        const onClose = (code: number | null, signal: NodeJS.Signals | null) => {
          try {
            frames.finish()
            fail(new Error(`The native Copilot catalog process exited before its reply: code ${code}, signal ${signal}.`))
          }
          catch (cause) { fail(cause) }
        }
        const onEnd = () => {
          try {
            frames.finish()
            if (!settled) {
              endedWithoutReply = new Error('The native Copilot catalog stream ended without a catalog reply.')
              fail(endedWithoutReply)
            }
          }
          catch (cause) { fail(cause) }
        }
        const onData = (chunk: Buffer) => {
          try {
            let catalog: string[] | undefined
            for (const frame of frames.push(chunk)) {
              validateCatalogEnvelope(frame)
              if (frame.id !== 1)
                continue
              if (Object.hasOwn(frame, 'method'))
                throw new Error('The native Copilot catalog reply has an invalid request envelope.')
              if (Object.hasOwn(frame, 'error'))
                throw new Error(`The native Copilot catalog request failed: ${JSON.stringify(frame.error)}`)
              if (catalog)
                throw new Error('The native Copilot catalog contains a repeated matching reply.')
              catalog = copilotCatalogNames(frame.result)
            }
            // The native server stays alive. Validate this complete chunk before accepting its one reply.
            if (catalog && !settled) {
              settled = true
              resolve(catalog)
            }
          }
          catch (cause) { fail(cause) }
        }
        detachListeners = () => {
          child.off('error', fail)
          child.off('close', onClose)
          child.stdout.off('data', onData)
          child.stdout.off('end', onEnd)
          child.stdout.off('error', fail)
          child.stdin.off('error', fail)
          child.stderr.off('error', fail)
        }
        child.once('error', fail)
        child.once('close', onClose)
        child.stdout.on('data', onData)
        child.stdout.once('end', onEnd)
        child.stdout.once('error', fail)
        child.stdin.once('error', fail)
        child.stderr.once('error', fail)
        child.stderr.resume()
        const deadline = () => fail(new Error('The complete native Copilot catalog exceeded its request deadline.'))
        if (runtime.scheduleDeadline) {
          cancelDeadline = runtime.scheduleDeadline(deadline)
        }
        else {
          const timer = setTimeout(deadline, CATALOG_REQUEST_DEADLINE_MS)
          cancelDeadline = () => clearTimeout(timer)
        }
        const body = Buffer.from(JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'tools.list', params: launch.model ? { model: launch.model } : {} }))
        child.stdin.write(Buffer.concat([Buffer.from(`Content-Length: ${body.byteLength}\r\n\r\n`), body]), (error) => {
          if (error)
            fail(error)
        })
      })
    }
    finally {
      cancelDeadline?.()
      detachListeners?.()
    }
  }, async () => {
    try {
      await (runtime.stop ?? stopProcess)(child)
    }
    finally {
      // Reject EOF immediately, then retain the actual exit status after the existing process cleanup completes.
      if (endedWithoutReply)
        endedWithoutReply.message = `The native Copilot catalog stream ended without a catalog reply: code ${child.exitCode}, signal ${child.signalCode}.`
    }
  })
}

/** Ask the installed native server for its complete builtin metadata without sending a model prompt. */
export async function readCopilotBuiltinCatalog(context: ManagedNativeScenarioContext): Promise<string[]> {
  const environment = context.leapmuxServer.agentEnv
  if (!environment?.HOME)
    throw new Error('The complete Copilot catalog requires the isolated native environment.')
  // The lookup reads the environment of the spawn, so it finds the executable that the spawn starts.
  const env = hubSpawnEnv(environment)
  const binary = requireBinary('copilot', 'The native Copilot catalog requires its installed CLI', env)
  const agent = await currentNativeAgent(context)
  const model = nativeOptionValue(agent, 'model')
  return queryCopilotBuiltinCatalog({ executable: binary, args: ['--server', '--stdio', '--no-remote', '--no-remote-export'], cwd: agent.workingDir, env, ...(model === undefined ? {} : { model }) })
}
