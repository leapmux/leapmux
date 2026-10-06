import type { Buffer } from 'node:buffer'
import type { ChildProcess } from 'node:child_process'
import type { ServerInfo } from '../fixtures'
import type { ServerOutput } from './serverOutput'
import { rmSync } from 'node:fs'
import { deregisterWorkerViaAPI, listOnlineWorkerIDsViaAPI, mintRegistrationKeyViaAPI, waitForNewOnlineWorkerViaAPI } from './api'
import { cleanupOnFailure, finishCleanup, withCleanup } from './cleanup'
import { stopProcess } from './process'
import { spawnTestProcess } from './processRegistry'
import { createTestDirectory } from './runDirectory'
import { getGlobalState, hubSpawnEnv } from './server'
import { createServerOutput } from './serverOutput'

/** The command line, the environment, and the output capture of one Worker process. */
export interface WorkerProcessOptions {
  /** The Hub that the Worker connects to. */
  hubUrl: string
  /** The Worker name. The Worker reads it from `LEAPMUX_WORKER_NAME`, and the output capture uses it as the label. */
  name: string
  /** The data directory of the Worker. The caller creates it and removes it. */
  dataDir: string
  /** The environment of the Worker, on top of `hubSpawnEnv`. */
  env?: NodeJS.ProcessEnv
  /** The arguments after `--hub` and `--data-dir`, such as `['--encryption-mode', 'post-quantum']`. */
  extraArgs?: readonly string[]
  /** The buffer that captures the stdout and the stderr of the Worker. */
  output: ServerOutput
  /**
   * Start the Worker in its own process group, and let the test process exit without a wait for it.
   * The process group protects the Worker from a signal to the test runner.
   */
  detached?: boolean
}

/**
 * Spawn one Worker process and capture its output.
 * With no registration key in `extraArgs`, the Worker uses the registration that its data directory holds.
 */
export function spawnWorkerProcess(options: WorkerProcessOptions): ChildProcess {
  const proc = spawnTestProcess(getGlobalState().binaryPath, [
    'worker',
    '--hub',
    options.hubUrl,
    '--data-dir',
    options.dataDir,
    ...options.extraArgs ?? [],
  ], {
    stdio: ['ignore', 'pipe', 'pipe'],
    detached: options.detached ?? false,
    env: hubSpawnEnv({ ...options.env, LEAPMUX_WORKER_NAME: options.name }),
  })
  if (options.detached)
    proc.unref()
  options.output.capture(proc, options.name)
  return proc
}

/** The Hub that a Worker registers with, and an administrator session on it. */
export type WorkerHub = Pick<ServerInfo, 'hubUrl' | 'adminToken'>

export interface RegisteredWorkerOptions extends Omit<WorkerProcessOptions, 'hubUrl'> {
  /**
   * Receive the process right after the spawn, before the online wait and before any output arrives.
   * A caller uses it to track the process or to read its streams. The registration still stops the process when it
   * fails.
   */
  onSpawn?: (proc: ChildProcess) => void
}

/** A Worker process that the Hub lists online, with the Worker ID that the Hub assigned. */
export interface RegisteredWorker {
  proc: ChildProcess
  workerId: string
}

/**
 * Register a new Worker with `hub`: mint a registration key, spawn the Worker with it, and wait for the Hub to list
 * the new Worker online.
 * The wait fails at once when the process fails or exits. After the spawn, a failure stops the process.
 *
 * The new Worker is the one online Worker ID that the Hub did not list before the spawn. Two concurrent
 * registrations with one Hub can therefore select the same Worker, so a caller must serialize them.
 */
export async function spawnRegisteredWorker(hub: WorkerHub, options: RegisteredWorkerOptions): Promise<RegisteredWorker> {
  const registrationKey = await mintRegistrationKeyViaAPI(hub.hubUrl, hub.adminToken)
  const previousWorkers = new Set(await listOnlineWorkerIDsViaAPI(hub.hubUrl, hub.adminToken))
  const { onSpawn, extraArgs = [], ...processOptions } = options
  const proc = spawnWorkerProcess({
    ...processOptions,
    hubUrl: hub.hubUrl,
    extraArgs: ['--registration-key', registrationKey, ...extraArgs],
  })
  const subject = `The Worker ${options.name}`
  return cleanupOnFailure(async () => {
    onSpawn?.(proc)
    const exit = watchProcessExit(proc, subject)
    const online = new AbortController()
    void exit.failure.catch((error: unknown) => online.abort(error))
    try {
      requireRunning(proc, subject)
      const workerId = await Promise.race([
        waitForNewOnlineWorkerViaAPI(hub.hubUrl, hub.adminToken, previousWorkers, undefined, online.signal),
        exit.failure,
      ])
      requireRunning(proc, subject)
      return { proc, workerId }
    }
    finally {
      exit.dispose()
      online.abort(new Error(`The online wait of ${subject} ended.`))
    }
  }, () => stopProcess(proc))
}

/** A promise that rejects when a process fails or exits, until `dispose` removes its listeners. */
interface ProcessExitWatch {
  failure: Promise<never>
  dispose: () => void
}

function watchProcessExit(proc: ChildProcess, subject: string): ProcessExitWatch {
  let rejectFailure!: (error: Error) => void
  const failure = new Promise<never>((_, reject) => {
    rejectFailure = reject
  })
  // A failure that no race reads any more must not become an unhandled rejection.
  failure.catch(() => {})
  const onError = (error: Error) => rejectFailure(error)
  const onExit = (code: number | null, signal: NodeJS.Signals | null) => {
    rejectFailure(new Error(`${subject} exited: code ${code}, signal ${signal}.`))
  }
  proc.on('error', onError)
  proc.on('exit', onExit)
  return {
    failure,
    dispose: () => {
      proc.off('error', onError)
      proc.off('exit', onExit)
    },
  }
}

/** Refuse a process that exited already. Its exit event fired before a watch could see it. */
function requireRunning(proc: ChildProcess, subject: string): void {
  if (proc.exitCode !== null || proc.signalCode !== null)
    throw new Error(`${subject} exited already: code ${proc.exitCode}, signal ${proc.signalCode}.`)
}

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
  const dataDir = createTestDirectory(`${options.dataDirPrefix}-`)
  const output = createServerOutput()
  // The registration sets these from its callback, which the control flow analysis of TypeScript cannot follow.
  const worker: { proc?: ChildProcess, id?: string } = {}
  try {
    await withCleanup(async () => {
      const registered = await spawnRegisteredWorker(server, {
        name: options.workerName,
        dataDir,
        env: { ...server.agentEnv, ...options.env },
        output,
        onSpawn: (proc) => {
          worker.proc = proc
          forwardStdio(proc, options)
        },
      })
      worker.id = registered.workerId
      const exit = watchProcessExit(registered.proc, 'The private Worker')
      try {
        await Promise.race([
          use({ server: { ...server, workerId: registered.workerId, agentEnv }, workerId: registered.workerId, dataDir, output }),
          exit.failure,
        ])
      }
      finally {
        exit.dispose()
      }
    }, async () => {
      const { proc, id } = worker
      if (!proc)
        return
      // The registration stops a Worker that it could not register, and a second stop of an exited process returns
      // at once. This stop still runs, because `afterStop` must see each stopped Worker.
      await finishCleanup([
        (async () => {
          await stopProcess(proc)
          await options.afterStop?.(proc)
        })(),
        id ? deregisterWorkerViaAPI(server.hubUrl, server.adminToken, id) : Promise.resolve(),
      ])
    })
  }
  catch (error) {
    throw new Error(`The private Worker failed.\n${output.since(0)}`, { cause: error })
  }
  finally {
    // Keep a live Worker's files if stop fails. Process cleanup can still need those files.
    const { proc } = worker
    if (!proc || proc.exitCode !== null || proc.signalCode !== null)
      rmSync(dataDir, { recursive: true, force: true })
  }
}

/** Forward each chunk of the Worker output, and the end of each stream, to the callbacks of the caller. */
function forwardStdio(proc: ChildProcess, options: Pick<NativeWorkerOptions, 'onStdio' | 'onStdioEnd'>): void {
  proc.stdout?.on('data', (chunk: Buffer) => options.onStdio?.(chunk, 'stdout'))
  proc.stderr?.on('data', (chunk: Buffer) => options.onStdio?.(chunk, 'stderr'))
  proc.stdout?.once('end', () => options.onStdioEnd?.('stdout'))
  proc.stderr?.once('end', () => options.onStdioEnd?.('stderr'))
  proc.once('close', () => {
    options.onStdioEnd?.('stdout')
    options.onStdioEnd?.('stderr')
  })
}
