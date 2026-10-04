import type { Buffer } from 'node:buffer'
import type { ChildProcess } from 'node:child_process'
import type { ServerInfo } from '../fixtures'
import type { ServerOutput } from './serverOutput'
import { rmSync } from 'node:fs'
import { deregisterWorkerViaAPI, listOnlineWorkerIDsViaAPI, mintRegistrationKeyViaAPI, waitForNewOnlineWorkerViaAPI } from './api'
import { finishCleanup, withCleanup } from './cleanup'
import { stopProcess } from './process'
import { spawnTestProcess } from './processRegistry'
import { createTestDirectory } from './runDirectory'
import { getGlobalState, hubSpawnEnv } from './server'
import { createServerOutput } from './serverOutput'

type NativeWorkerServer = Pick<ServerInfo, 'hubUrl' | 'adminToken' | 'workerId'> & Partial<Pick<ServerInfo, 'agentEnv'>>

export interface NativeWorker<Server extends NativeWorkerServer> {
  server: Omit<Server, 'workerId' | 'agentEnv'> & { workerId: string, agentEnv: Record<string, string> }
  workerId: string
  dataDir: string
  output: ServerOutput
}

export interface NativeWorkerOptions {
  dataDirPrefix: string
  workerName: string
  env?: NodeJS.ProcessEnv
  onStdio?: (chunk: Buffer, stream: 'stdout' | 'stderr') => void
  onStdioEnd?: (stream: 'stdout' | 'stderr') => void
  /** Validate physical process cleanup after stop succeeds. Deregistration still runs if validation fails. */
  afterStop?: (process: ChildProcess) => void | Promise<void>
}

/** Register one private Worker with the suite Hub and retain all failure diagnostics. */
export async function withNativeWorker<Server extends NativeWorkerServer>(
  server: Server,
  options: NativeWorkerOptions,
  use: (worker: NativeWorker<Server>) => Promise<void>,
): Promise<void> {
  if (!server.agentEnv || !server.agentEnv.HOME)
    throw new Error('The private Worker requires the isolated agent environment and HOME.')
  if (!options.workerName.trim() || !options.dataDirPrefix.trim())
    throw new Error('The private Worker requires a name and a data directory prefix.')
  const agentEnv: Record<string, string> = {}
  for (const [key, value] of Object.entries({ ...server.agentEnv, ...options.env })) {
    if (typeof value === 'string')
      agentEnv[key] = value
  }
  if (!agentEnv.HOME?.trim())
    throw new Error('The private Worker environment requires a nonempty isolated HOME.')
  const registrationKey = await mintRegistrationKeyViaAPI(server.hubUrl, server.adminToken)
  const previousWorkers = new Set(await listOnlineWorkerIDsViaAPI(server.hubUrl, server.adminToken))
  const dataDir = createTestDirectory(`${options.dataDirPrefix}-`)
  const output = createServerOutput()
  let proc: ChildProcess | undefined
  let workerId: string | undefined
  try {
    proc = spawnTestProcess(getGlobalState().binaryPath, [
      'worker',
      '--hub',
      server.hubUrl,
      '--registration-key',
      registrationKey,
      '--data-dir',
      dataDir,
    ], {
      stdio: ['ignore', 'pipe', 'pipe'],
      env: hubSpawnEnv({ ...server.agentEnv, ...options.env, LEAPMUX_WORKER_NAME: options.workerName }),
    })
    output.capture(proc, options.workerName)
    const process = proc
    const onlineAbort = new AbortController()
    let closing = false
    let rejectProcessFailure!: (error: Error) => void
    const processFailure = new Promise<never>((_, reject) => {
      rejectProcessFailure = reject
    })
    const onProcessError = (error: Error) => {
      if (closing)
        return
      onlineAbort.abort(error)
      rejectProcessFailure(error)
    }
    const onProcessExit = (code: number | null, signal: NodeJS.Signals | null) => {
      onProcessError(new Error(`The private Worker exited before its scenario ended: code ${code}, signal ${signal}.`))
    }
    const requireRunningProcess = () => {
      if (process.exitCode !== null || process.signalCode !== null)
        throw new Error(`The private Worker already exited: code ${process.exitCode}, signal ${process.signalCode}.`)
    }
    process.on('error', onProcessError)
    process.on('exit', onProcessExit)
    process.stdout?.on('data', (chunk: Buffer) => options.onStdio?.(chunk, 'stdout'))
    process.stderr?.on('data', (chunk: Buffer) => options.onStdio?.(chunk, 'stderr'))
    process.stdout?.once('end', () => options.onStdioEnd?.('stdout'))
    process.stderr?.once('end', () => options.onStdioEnd?.('stderr'))
    process.once('close', () => {
      options.onStdioEnd?.('stdout')
      options.onStdioEnd?.('stderr')
    })
    try {
      await withCleanup(async () => {
        requireRunningProcess()
        workerId = await Promise.race([
          waitForNewOnlineWorkerViaAPI(server.hubUrl, server.adminToken, previousWorkers, undefined, onlineAbort.signal),
          processFailure,
        ])
        requireRunningProcess()
        await Promise.race([
          use({ server: { ...server, workerId, agentEnv }, workerId, dataDir, output }),
          processFailure,
        ])
      }, async () => {
        closing = true
        onlineAbort.abort(new Error('The private Worker online wait ended.'))
        await finishCleanup([
          (async () => {
            await stopProcess(process)
            await options.afterStop?.(process)
          })(),
          workerId ? deregisterWorkerViaAPI(server.hubUrl, server.adminToken, workerId) : Promise.resolve(),
        ])
      })
    }
    finally {
      process.off('error', onProcessError)
      process.off('exit', onProcessExit)
    }
  }
  catch (error) {
    throw new Error(`The private Worker failed.\n${output.since(0)}`, { cause: error })
  }
  finally {
    // Keep a live Worker's files if stop fails. Process cleanup can still need those files.
    if (!proc || proc.exitCode !== null || proc.signalCode !== null)
      rmSync(dataDir, { recursive: true, force: true })
  }
}
