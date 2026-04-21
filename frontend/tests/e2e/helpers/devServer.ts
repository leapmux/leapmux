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
import { getAdminOrgId, getWorkerId, loginViaAPI } from './api'
import { findFreePort, getGlobalState, waitForServer } from './server'

export interface DevServerHandle {
  hubUrl: string
  adminToken: string
  adminOrgId: string
  workerId: string
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
  const { binaryPath } = getGlobalState()
  const dataDir = mkdtempSync(join(tmpdir(), `${opts.dataDirPrefix ?? 'leapmux-e2e-'}-`))
  const port = await findFreePort()
  const hubUrl = `http://localhost:${port}`

  const proc = spawn(binaryPath, ['dev', '-addr', `:${port}`, '-data-dir', dataDir], {
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
  const adminToken = await loginViaAPI(hubUrl, 'admin', 'admin123')
  const adminOrgId = await getAdminOrgId(hubUrl, adminToken)
  const workerId = await getWorkerId(hubUrl, adminToken)
  return { hubUrl, adminToken, adminOrgId, workerId, proc, dataDir }
}

export async function stopDevServer(handle: DevServerHandle, extraPaths: string[] = []): Promise<void> {
  handle.proc.kill('SIGTERM')
  await new Promise(r => setTimeout(r, 1000))
  try {
    handle.proc.kill('SIGKILL')
  }
  catch { /* already dead */ }
  rmSync(handle.dataDir, { recursive: true, force: true })
  for (const p of extraPaths)
    rmSync(p, { recursive: true, force: true })
}
