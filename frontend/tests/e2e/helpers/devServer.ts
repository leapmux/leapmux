/**
 * Start a private dev instance for a test that cannot use the shared fixture.
 * Callers can supply custom environment settings or capture server output.
 * Examples include startup tracing, an invalid shell, and a shorter startup deadline.
 */
import type { Buffer } from 'node:buffer'
import type { ChildProcess } from 'node:child_process'
import { rmSync } from 'node:fs'
import {
  closeTestChannels,
  getUserId,
  getWorkerId,
  signUpViaAPI,
  TEST_ADMIN_DISPLAY_NAME,
  TEST_ADMIN_PASSWORD,
  TEST_ADMIN_USERNAME,
} from './api'
import { cleanupOnFailure, finishCleanup } from './cleanup'
import { stopProcess } from './process'
import { spawnTestProcess } from './processRegistry'
import { createTestDirectory } from './runDirectory'
import { findFreePort, getGlobalState, hubSpawnEnv, waitForServer } from './server'

export interface DevServerHandle {
  hubUrl: string
  adminToken: string
  /**
   * The administrator user ID.
   * Browser storage requires this ID before a test sets account preferences ahead of login.
   */
  adminUserId: string
  workerId: string
  proc: ChildProcess
  dataDir: string
}

export interface UnseededDevServerHandle {
  hubUrl: string
  proc: ChildProcess
  dataDir: string
}

export interface StartDevServerOptions {
  /** Extra env vars layered on top of process.env. */
  env?: Record<string, string | undefined>
  /** Prefix for the mkdtemp name (helps when debugging leftover dirs). */
  dataDirPrefix?: string
  /** Receive each stdout/stderr chunk (already `.resume()`d if absent). */
  onStdio?: (chunk: Buffer, stream: 'stdout' | 'stderr') => void
}

export async function startDevServer(opts: StartDevServerOptions = {}): Promise<DevServerHandle> {
  const unseeded = await startUnseededDevServer(opts)
  return cleanupOnFailure(async () => {
    const adminToken = await signUpViaAPI(unseeded.hubUrl, TEST_ADMIN_USERNAME, TEST_ADMIN_PASSWORD, TEST_ADMIN_DISPLAY_NAME)
    const adminUserId = await getUserId(unseeded.hubUrl, adminToken)
    const workerId = await getWorkerId(unseeded.hubUrl, adminToken)
    return { ...unseeded, adminToken, adminUserId, workerId }
  }, () => stopDevServer(unseeded))
}

/**
 * Like startDevServer, but does not register the initial admin. Use this for
 * specs that need to exercise the /setup flow directly.
 */
export async function startUnseededDevServer(opts: StartDevServerOptions = {}): Promise<UnseededDevServerHandle> {
  const { binaryPath } = getGlobalState()
  const dataDir = createTestDirectory(`${opts.dataDirPrefix ?? 'leapmux-e2e-'}-`)
  const port = await findFreePort()
  const hubUrl = `http://localhost:${port}`

  const proc = spawnTestProcess(binaryPath, ['dev', '-listen', `:${port}`, '-data-dir', dataDir], {
    stdio: ['ignore', 'pipe', 'pipe'],
    env: hubSpawnEnv(opts.env),
  })

  if (opts.onStdio) {
    proc.stdout?.on('data', c => opts.onStdio!(c, 'stdout'))
    proc.stderr?.on('data', c => opts.onStdio!(c, 'stderr'))
  }
  else {
    proc.stdout?.resume()
    proc.stderr?.resume()
  }

  const handle = { hubUrl, proc, dataDir }
  return cleanupOnFailure(async () => {
    await waitForServer(hubUrl)
    return handle
  }, () => stopDevServer(handle))
}

export async function stopDevServer(handle: DevServerHandle | UnseededDevServerHandle, extraPaths: string[] = []): Promise<void> {
  await finishCleanup([closeTestChannels(handle.hubUrl), stopProcess(handle.proc)])
  rmSync(handle.dataDir, { recursive: true, force: true })
  for (const p of extraPaths)
    rmSync(p, { recursive: true, force: true })
}

export interface SoloServerHandle {
  hubUrl: string
  /** The address `-listen` was given, so a spec can assert what the panel shows. */
  listen: string
  proc: ChildProcess
  dataDir: string
}

export interface StartSoloServerOptions extends StartDevServerOptions {
  /**
   * The host for -listen. The default 127.0.0.1 accepts only local connections.
   * Use 0.0.0.0 to test an exposed hub. Loopback requests also reach this wildcard listener.
   * The test then needs no separate interface address, which a CI host might not provide.
   */
  listenHost?: string
}

/**
 * Start a private solo instance.
 * Solo creates one account called solo through bootstrap.Run. No administrator registration is necessary.
 * Its initial password and network-access rules differ from dev mode, which requires password authentication immediately.
 * A TCP client must set the first password before the app loads. Only the desktop local IPC socket permits access without a credential.
 */
export async function startSoloServer(opts: StartSoloServerOptions = {}): Promise<SoloServerHandle> {
  const { binaryPath } = getGlobalState()
  const dataDir = createTestDirectory(`${opts.dataDirPrefix ?? 'leapmux-e2e-solo'}-`)
  const port = await findFreePort()
  const listen = `${opts.listenHost ?? '127.0.0.1'}:${port}`
  const hubUrl = `http://127.0.0.1:${port}`

  const proc = spawnTestProcess(binaryPath, ['solo', '-listen', listen, '-data-dir', dataDir], {
    stdio: ['ignore', 'pipe', 'pipe'],
    env: hubSpawnEnv(opts.env),
  })

  if (opts.onStdio) {
    proc.stdout?.on('data', c => opts.onStdio!(c, 'stdout'))
    proc.stderr?.on('data', c => opts.onStdio!(c, 'stderr'))
  }
  else {
    proc.stdout?.resume()
    proc.stderr?.resume()
  }

  const handle = { hubUrl, listen, proc, dataDir }
  return cleanupOnFailure(async () => {
    await waitForServer(hubUrl)
    return handle
  }, () => stopSoloServer(handle))
}

/**
 * Stop a solo instance. Return immediately when no handle exists.
 * Playwright calls afterEach after a failed beforeEach. Accessing an absent handle would hide the startup failure with a TypeError.
 */
export async function stopSoloServer(handle: SoloServerHandle | undefined): Promise<void> {
  if (!handle)
    return
  await finishCleanup([closeTestChannels(handle.hubUrl), stopProcess(handle.proc)])
  rmSync(handle.dataDir, { recursive: true, force: true })
}
