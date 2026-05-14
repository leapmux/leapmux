/**
 * Multi-worker harness for tests that exercise the cross-worker
 * code path the plan calls "the normal case" — workspaces whose
 * tabs span more than one worker.
 *
 * The harness spawns a standalone `leapmux hub` (no embedded
 * worker — `dev` and `solo` modes pre-bake one) plus N worker
 * processes, each registered to the hub via a freshly minted
 * registration key. Each worker gets its own data dir so they
 * don't fight over SQLite locks, and the hub gets one too.
 *
 * Why a separate harness instead of extending `leapmuxServer`?
 *
 * - `leapmuxServer` runs `leapmux dev`, which is the single-process
 *   solo mode flipped into multi-user. It deliberately bundles the
 *   worker into the hub process. Extending it to spawn additional
 *   workers would either fork the dev fixture or smuggle worker
 *   spawning into the same fixture, neither of which is clean.
 * - The cross-worker path needs distinct (worker_id, public_key)
 *   pairs so the hub-side `verifyWorkerAccess` and worker-to-worker
 *   E2EE pin store have something real to assert against.
 * - The harness can grow in scope (worker failover, account
 *   migration) without polluting the single-worker fixture.
 *
 * Lifetime: the harness is constructed by a test fixture, kept
 * for the duration of one spec file (worker scope), and torn down
 * via `Stop()` which kills every spawned process and removes the
 * temp directories.
 */

import type { ChildProcess } from 'node:child_process'
import { spawn } from 'node:child_process'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import process from 'node:process'
import {
  authedHeaders,
  getAdminOrgId,
  signUpViaAPI,
  TEST_ADMIN_DISPLAY_NAME,
  TEST_ADMIN_PASSWORD,
  TEST_ADMIN_USERNAME,
} from './api'
import { findFreePort, getGlobalState, waitForServer } from './server'

/** A single worker spawned by the harness. */
export interface HarnessWorker {
  /** Worker id allocated by the hub on registration. */
  id: string
  /** Display name passed to `--name`. */
  name: string
  /** Per-worker data dir; cleaned up at Stop(). */
  dataDir: string
  /** Process handle for the running `leapmux worker` invocation. */
  proc: ChildProcess
}

/** A multi-worker hub harness; create with `startMultiWorkerHarness`. */
export interface MultiWorkerHarness {
  hubUrl: string
  hubDataDir: string
  hubProc: ChildProcess
  adminToken: string
  adminOrgId: string
  /** Workers in the order they were spawned. Always at least one. */
  workers: HarnessWorker[]
  /** Spawn an additional worker registered to the same hub. */
  addWorker: (name: string) => Promise<HarnessWorker>
  /** Tear everything down. Idempotent. */
  stop: () => Promise<void>
}

/**
 * Spawn `leapmux hub` plus `count` workers and return a handle.
 * `count` defaults to 2 — the minimum that exercises the
 * cross-worker code path. Tests can call `harness.addWorker()` to
 * grow the cluster mid-spec.
 */
export async function startMultiWorkerHarness(count = 2): Promise<MultiWorkerHarness> {
  const { binaryPath } = getGlobalState()

  const hubDataDir = mkdtempSync(join(tmpdir(), 'leapmux-mw-hub-'))
  const port = await findFreePort()
  // `localhost` (not `127.0.0.1`) so cookies set by `loginViaToken`
  // (which hardcodes `domain: 'localhost'`) are accepted by the
  // browser for our hub URL. Mismatching the hostnames means the
  // frontend ships requests without auth and silently lands on a
  // blank page — exactly the failure mode we'd otherwise debug for
  // an hour.
  const hubUrl = `http://localhost:${port}`

  // eslint-disable-next-line no-console
  console.log(`[mw] starting hub on port ${port} (data=${hubDataDir})`)
  const hubProc = spawn(binaryPath, [
    'hub',
    '-listen',
    `:${port}`,
    '-data-dir',
    hubDataDir,
  ], {
    stdio: ['ignore', 'pipe', 'pipe'],
    env: {
      ...process.env,
      // Disable shell quirks that would otherwise leak into worker
      // spawn paths. Multi-worker tests don't drive real agents,
      // they just exercise the channel and event plumbing.
      LEAPMUX_HUB_SIGNUP_ENABLED: 'true',
    },
  })
  hubProc.stdout?.resume()
  hubProc.stderr?.resume()
  await waitForServer(hubUrl)

  // Bootstrap the admin user. signUpViaAPI returns the session
  // cookie, which is what authedHeaders / getAdminOrgId expect.
  const adminToken = await signUpViaAPI(hubUrl, TEST_ADMIN_USERNAME, TEST_ADMIN_PASSWORD, TEST_ADMIN_DISPLAY_NAME)
  const adminOrgId = await getAdminOrgId(hubUrl, adminToken)

  // Track every spawned process so cleanup catches partial-init
  // failures.
  const workers: HarnessWorker[] = []
  let stopped = false

  async function spawnWorker(name: string): Promise<HarnessWorker> {
    const dataDir = mkdtempSync(join(tmpdir(), `leapmux-mw-w-${name}-`))
    const regKey = await mintRegistrationKey(hubUrl, adminToken)

    // ListWorkers identifies workers by id, not display name, so
    // diff before vs. after to figure out which row belongs to the
    // worker we just spawned. The before-snapshot is taken AFTER
    // mintRegistrationKey so a slow registration on a previous
    // worker can't pollute the diff.
    const before = new Set(await listOnlineWorkerIDs(hubUrl, adminToken))

    // eslint-disable-next-line no-console
    console.log(`[mw] starting worker ${name} (data=${dataDir})`)
    const proc = spawn(binaryPath, [
      'worker',
      '--hub',
      hubUrl,
      '--registration-key',
      regKey,
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
    proc.stdout?.resume()
    proc.stderr?.resume()

    const workerId = await waitForNewOnlineWorker(hubUrl, adminToken, before)

    const w: HarnessWorker = { id: workerId, name, dataDir, proc }
    workers.push(w)
    return w
  }

  async function stop(): Promise<void> {
    if (stopped)
      return
    stopped = true
    for (const w of workers) {
      try {
        w.proc.kill('SIGTERM')
      }
      catch {
        // already dead
      }
    }
    try {
      hubProc.kill('SIGTERM')
    }
    catch {
      // already dead
    }
    await new Promise(r => setTimeout(r, 1000))
    for (const w of workers) {
      try {
        w.proc.kill('SIGKILL')
      }
      catch {
        // already dead
      }
      rmSync(w.dataDir, { recursive: true, force: true })
    }
    try {
      hubProc.kill('SIGKILL')
    }
    catch {
      // already dead
    }
    rmSync(hubDataDir, { recursive: true, force: true })
  }

  try {
    for (let i = 0; i < count; i++)
      await spawnWorker(`worker-${String.fromCharCode(65 + i)}`) // A, B, C, …
  }
  catch (err) {
    await stop()
    throw err
  }

  return {
    hubUrl,
    hubDataDir,
    hubProc,
    adminToken,
    adminOrgId,
    workers,
    addWorker: spawnWorker,
    stop,
  }
}

/**
 * Mint a registration key as the admin user. Mirrors the production
 * UI flow: an authenticated user calls
 * `WorkerManagementService.CreateRegistrationKey`, then hands the
 * resulting key to the worker process via `--registration-key`.
 */
async function mintRegistrationKey(hubUrl: string, cookie: string): Promise<string> {
  const res = await fetch(`${hubUrl}/leapmux.v1.WorkerManagementService/CreateRegistrationKey`, {
    method: 'POST',
    headers: authedHeaders(cookie),
    body: '{}',
  })
  if (!res.ok)
    throw new Error(`mintRegistrationKey: ${res.status} ${await res.text()}`)
  const data = await res.json() as { registrationKey?: string }
  if (!data.registrationKey)
    throw new Error('mintRegistrationKey: empty key in response')
  return data.registrationKey
}

/** List the IDs of every currently-online worker visible to the admin. */
async function listOnlineWorkerIDs(hubUrl: string, cookie: string): Promise<string[]> {
  const res = await fetch(`${hubUrl}/leapmux.v1.WorkerManagementService/ListWorkers`, {
    method: 'POST',
    headers: authedHeaders(cookie),
    body: '{}',
  })
  if (!res.ok)
    throw new Error(`listOnlineWorkerIDs: ListWorkers ${res.status}`)
  const data = await res.json() as { workers?: Array<{ id: string, online: boolean }> }
  return (data.workers ?? []).filter(w => w.online).map(w => w.id)
}

/**
 * Poll `ListWorkers` until a worker that was NOT in `before` shows
 * up online. 30s budget — first-time registration, key derivation,
 * and bidi-stream attach can take a few seconds on cold caches.
 */
async function waitForNewOnlineWorker(hubUrl: string, cookie: string, before: Set<string>, timeoutMs = 30_000): Promise<string> {
  const deadline = Date.now() + timeoutMs
  while (true) {
    const ids = await listOnlineWorkerIDs(hubUrl, cookie)
    const fresh = ids.find(id => !before.has(id))
    if (fresh)
      return fresh
    if (Date.now() >= deadline)
      throw new Error(`waitForNewOnlineWorker: no new worker came online within ${timeoutMs}ms (saw: ${ids.join(', ')})`)
    await new Promise(r => setTimeout(r, 500))
  }
}
