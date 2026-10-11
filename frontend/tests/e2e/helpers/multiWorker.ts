import type { ChildProcess } from 'node:child_process'
import type { ServerOutput } from './serverOutput'
import { rmSync } from 'node:fs'
import { join } from 'node:path'
import {
  closeTestChannels,
  getUserId,
  signUpViaAPI,
  TEST_ADMIN_DISPLAY_NAME,
  TEST_ADMIN_PASSWORD,
  TEST_ADMIN_USERNAME,
} from './api'
import { cleanupOnFailure, finishCleanup } from './cleanup'
import { spawnRegisteredWorker } from './nativeWorker'
import { stopProcess, stopProcesses } from './process'
import { spawnTestProcess } from './processRegistry'
import { createTestDirectory } from './runDirectory'
import { getGlobalState, hubSpawnEnv, waitForHubStart } from './server'
import { createServerOutput, reportStartupFailure } from './serverOutput'

/** A registered worker with its own process and database directory. */
export interface HarnessWorker {
  id: string
  name: string
  dataDir: string
  proc: ChildProcess
}

/** A standalone hub with separate worker identities and encryption keys. */
export interface MultiWorkerHarness {
  hubUrl: string
  hubDataDir: string
  hubProc: ChildProcess
  adminToken: string
  /** Browser storage requires the account ID before a test seeds preferences. */
  adminUserId: string
  /** Registered workers in launch order. */
  workers: HarnessWorker[]
  /** The output of the hub and of each worker. Each line starts with the name of its process. */
  output: ServerOutput
  addWorker: (name: string) => Promise<HarnessWorker>
  /** Concurrent calls share the same cleanup operation. */
  stop: () => Promise<void>
}

/**
 * Start a hub and workers with separate databases. Close all resources after a startup failure.
 * A failed startup, and a failed later addition, print the output of every process before the error propagates.
 */
export async function startMultiWorkerHarness(count = 2): Promise<MultiWorkerHarness> {
  if (!Number.isSafeInteger(count) || count < 0)
    throw new RangeError('The worker count must be a nonnegative integer')
  const { binaryPath } = getGlobalState()
  const hubDataDir = createTestDirectory('leapmux-mw-hub-')
  const directories = new Set([hubDataDir])
  // Include unregistered children so a failed handshake cannot leave a process alive.
  const processes = new Set<ChildProcess>()
  const output = createServerOutput()
  let additions: Promise<void> = Promise.resolve()
  let shutdown: Promise<void> | undefined
  // The startup reports its own failure once, so only an addition after the startup reports its failure.
  let started = false
  let hubUrl = ''

  function stop(): Promise<void> {
    shutdown ??= (async () => {
      await additions
      await finishCleanup([
        hubUrl ? closeTestChannels(hubUrl) : Promise.resolve(),
        stopProcesses([...processes]),
      ])
      for (const directory of directories)
        rmSync(directory, { recursive: true, force: true })
    })()
    return shutdown
  }

  return cleanupOnFailure(async () => {
    const hubProc = spawnTestProcess(binaryPath, ['hub', '-listen', '127.0.0.1:0', '-data-dir', hubDataDir], {
      stdio: ['ignore', 'pipe', 'pipe'],
      env: hubSpawnEnv(),
    })
    processes.add(hubProc)
    output.capture(hubProc, 'hub')
    // The localhost URL matches the cookies that loginViaToken installs.
    hubUrl = (await waitForHubStart(join(hubDataDir, 'state.json'), hubProc)).hubUrl
    const adminToken = await signUpViaAPI(hubUrl, TEST_ADMIN_USERNAME, TEST_ADMIN_PASSWORD, TEST_ADMIN_DISPLAY_NAME)
    const adminUserId = await getUserId(hubUrl, adminToken)
    const workers: HarnessWorker[] = []

    async function spawnWorker(name: string): Promise<HarnessWorker> {
      const dataDir = createTestDirectory(`leapmux-mw-w-${name}-`)
      directories.add(dataDir)
      let proc: ChildProcess | undefined
      return cleanupOnFailure(async () => {
        const registered = await spawnRegisteredWorker({ hubUrl, adminToken }, {
          name,
          dataDir,
          extraArgs: ['--encryption-mode', 'post-quantum'],
          output,
          onSpawn: (spawned) => {
            proc = spawned
            processes.add(spawned)
          },
        })
        const worker = { id: registered.workerId, name, dataDir, proc: registered.proc }
        workers.push(worker)
        return worker
      }, async () => {
        // The registration stops its own process when it fails. This stop returns at once for an exited process, and
        // it still waits for a process whose own stop failed.
        if (proc) {
          await stopProcess(proc)
          processes.delete(proc)
        }
        rmSync(dataDir, { recursive: true, force: true })
        directories.delete(dataDir)
      })
    }

    function addWorker(name: string): Promise<HarnessWorker> {
      if (shutdown)
        return Promise.reject(new Error('The multi-worker harness is stopped'))
      // Registration uses a before/after ID comparison. Concurrent snapshots can select the same worker.
      const attempt = additions.then(() => spawnWorker(name))
      // The caller receives the failure. The queue must remain available for subsequent additions and cleanup.
      additions = attempt.then(() => {}, () => {})
      return started ? attempt.catch(error => reportStartupFailure(output, `The worker ${name} of the multi-worker harness`, error)) : attempt
    }

    for (let i = 0; i < count; i++)
      await addWorker(`worker-${String.fromCharCode(65 + i)}`)
    started = true
    return { hubUrl, hubDataDir, hubProc, adminToken, adminUserId, workers, output, addWorker, stop }
  }, stop).catch(error => reportStartupFailure(output, 'The multi-worker harness', error))
}
