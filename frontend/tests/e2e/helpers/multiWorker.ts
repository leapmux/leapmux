import type { ChildProcess } from 'node:child_process'
import { rmSync } from 'node:fs'
import process from 'node:process'
import {
  closeTestChannels,
  getUserId,
  listOnlineWorkerIDsViaAPI,
  mintRegistrationKeyViaAPI,
  signUpViaAPI,
  TEST_ADMIN_DISPLAY_NAME,
  TEST_ADMIN_PASSWORD,
  TEST_ADMIN_USERNAME,
  waitForNewOnlineWorkerViaAPI,
} from './api'
import { cleanupOnFailure, finishCleanup } from './cleanup'
import { stopProcess, stopProcesses } from './process'
import { spawnTestProcess } from './processRegistry'
import { createTestDirectory } from './runDirectory'
import { findFreePort, getGlobalState, hubSpawnEnv, waitForServer } from './server'

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
  addWorker: (name: string) => Promise<HarnessWorker>
  /** Concurrent calls share the same cleanup operation. */
  stop: () => Promise<void>
}

/** Start a hub and workers with separate databases. Close all resources after a startup failure. */
export async function startMultiWorkerHarness(count = 2): Promise<MultiWorkerHarness> {
  if (!Number.isSafeInteger(count) || count < 0)
    throw new RangeError('The worker count must be a nonnegative integer')
  const { binaryPath } = getGlobalState()
  const hubDataDir = createTestDirectory('leapmux-mw-hub-')
  const directories = new Set([hubDataDir])
  // Include unregistered children so a failed handshake cannot leave a process alive.
  const processes = new Set<ChildProcess>()
  let additions: Promise<void> = Promise.resolve()
  let shutdown: Promise<void> | undefined
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
    const port = await findFreePort()
    // The hostname must match the localhost cookies that loginViaToken installs.
    hubUrl = `http://localhost:${port}`
    const hubProc = spawnTestProcess(binaryPath, ['hub', '-listen', `:${port}`, '-data-dir', hubDataDir], {
      stdio: ['ignore', 'pipe', 'pipe'],
      env: hubSpawnEnv(),
    })
    processes.add(hubProc)
    hubProc.stdout?.resume()
    hubProc.stderr?.resume()
    await waitForServer(hubUrl)
    const adminToken = await signUpViaAPI(hubUrl, TEST_ADMIN_USERNAME, TEST_ADMIN_PASSWORD, TEST_ADMIN_DISPLAY_NAME)
    const adminUserId = await getUserId(hubUrl, adminToken)
    const workers: HarnessWorker[] = []

    async function spawnWorker(name: string): Promise<HarnessWorker> {
      const dataDir = createTestDirectory(`leapmux-mw-w-${name}-`)
      directories.add(dataDir)
      let proc: ChildProcess | undefined
      return cleanupOnFailure(async () => {
        const registrationKey = await mintRegistrationKeyViaAPI(hubUrl, adminToken)
        const before = new Set(await listOnlineWorkerIDsViaAPI(hubUrl, adminToken))
        proc = spawnTestProcess(binaryPath, [
          'worker',
          '--hub',
          hubUrl,
          '--registration-key',
          registrationKey,
          '--name',
          name,
          '--data-dir',
          dataDir,
          '--encryption-mode',
          'post-quantum',
        ], {
          stdio: ['ignore', 'pipe', 'pipe'],
          env: process.env,
        })
        processes.add(proc)
        proc.stdout?.resume()
        proc.stderr?.resume()
        const id = await waitForNewOnlineWorkerViaAPI(hubUrl, adminToken, before)
        const worker = { id, name, dataDir, proc }
        workers.push(worker)
        return worker
      }, async () => {
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
      return attempt
    }

    for (let i = 0; i < count; i++)
      await addWorker(`worker-${String.fromCharCode(65 + i)}`)
    return { hubUrl, hubDataDir, hubProc, adminToken, adminUserId, workers, addWorker, stop }
  }, stop)
}
