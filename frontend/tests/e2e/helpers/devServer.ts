/**
 * Shared spawn+teardown helper for specs that need their own `leapmux dev`
 * instance (instead of the shared fixture from tests/e2e/fixtures.ts).
 * Used by specs that require custom env (e.g. LEAPMUX_TRACE_AGENT_STARTUP,
 * a failing SHELL, a reduced startup timeout) or a private log buffer.
 */
import type { Buffer } from 'node:buffer'
import type { ChildProcess } from 'node:child_process'
import { spawn } from 'node:child_process'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import process from 'node:process'
import {
  getUserId,
  getWorkerId,
  signUpViaAPI,
  TEST_ADMIN_DISPLAY_NAME,
  TEST_ADMIN_PASSWORD,
  TEST_ADMIN_USERNAME,
} from './api'
import { stopProcess } from './process'
import { findFreePort, getGlobalState, waitForServer } from './server'

export interface DevServerHandle {
  hubUrl: string
  adminToken: string
  /**
   * The admin's user id. Every browser-storage key is scoped to an account, so
   * a spec that seeds a preference before the page signs in needs it up front.
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
  // Register the first admin via setup mode (dev mode no longer auto-bootstraps).
  const adminToken = await signUpViaAPI(unseeded.hubUrl, TEST_ADMIN_USERNAME, TEST_ADMIN_PASSWORD, TEST_ADMIN_DISPLAY_NAME)
  const adminUserId = await getUserId(unseeded.hubUrl, adminToken)
  const workerId = await getWorkerId(unseeded.hubUrl, adminToken)
  return { ...unseeded, adminToken, adminUserId, workerId }
}

/**
 * Like startDevServer, but does not register the initial admin. Use this for
 * specs that need to exercise the /setup flow directly.
 */
export async function startUnseededDevServer(opts: StartDevServerOptions = {}): Promise<UnseededDevServerHandle> {
  const { binaryPath } = getGlobalState()
  const dataDir = mkdtempSync(join(tmpdir(), `${opts.dataDirPrefix ?? 'leapmux-e2e-'}-`))
  const port = await findFreePort()
  const hubUrl = `http://localhost:${port}`

  const proc = spawn(binaryPath, ['dev', '-listen', `:${port}`, '-data-dir', dataDir], {
    stdio: ['ignore', 'pipe', 'pipe'],
    env: { ...process.env, ...opts.env },
  })

  if (opts.onStdio) {
    proc.stdout?.on('data', c => opts.onStdio!(c, 'stdout'))
    proc.stderr?.on('data', c => opts.onStdio!(c, 'stderr'))
  }
  else {
    proc.stdout?.resume()
    proc.stderr?.resume()
  }

  await waitForServer(hubUrl)
  return { hubUrl, proc, dataDir }
}

export async function stopDevServer(handle: DevServerHandle | UnseededDevServerHandle, extraPaths: string[] = []): Promise<void> {
  await stopProcess(handle.proc)
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
   * The host `-listen` binds. Defaults to `127.0.0.1`, which exposes nothing.
   *
   * A spec that needs the hub EXPOSED passes `0.0.0.0`. It reaches it over
   * loopback all the same -- a wildcard bind answers there too -- so the spec
   * needs no address of its own on this machine, which a CI runner may not
   * have.
   */
  listenHost?: string
}

/**
 * A `leapmux solo` instance of the spec's own.
 *
 * Solo is its own mode, not a flag on dev: it runs one account named `solo`
 * with no password, and its sign-in rule is what the network-access feature
 * changes. The shared fixture runs `leapmux dev`, which has real password
 * authentication from the start and therefore cannot exercise any of it.
 *
 * No admin registration: `bootstrap.Run` creates the account, and until it has
 * a password the hub authenticates every caller as it.
 */
export async function startSoloServer(opts: StartSoloServerOptions = {}): Promise<SoloServerHandle> {
  const { binaryPath } = getGlobalState()
  const dataDir = mkdtempSync(join(tmpdir(), `${opts.dataDirPrefix ?? 'leapmux-e2e-solo'}-`))
  const port = await findFreePort()
  const listen = `${opts.listenHost ?? '127.0.0.1'}:${port}`
  const hubUrl = `http://127.0.0.1:${port}`

  const proc = spawn(binaryPath, ['solo', '-listen', listen, '-data-dir', dataDir], {
    stdio: ['ignore', 'pipe', 'pipe'],
    env: { ...process.env, ...opts.env },
  })

  if (opts.onStdio) {
    proc.stdout?.on('data', c => opts.onStdio!(c, 'stdout'))
    proc.stderr?.on('data', c => opts.onStdio!(c, 'stderr'))
  }
  else {
    proc.stdout?.resume()
    proc.stderr?.resume()
  }

  await waitForServer(hubUrl)
  return { hubUrl, listen, proc, dataDir }
}

export async function stopSoloServer(handle: SoloServerHandle): Promise<void> {
  await stopProcess(handle.proc)
  rmSync(handle.dataDir, { recursive: true, force: true })
}
