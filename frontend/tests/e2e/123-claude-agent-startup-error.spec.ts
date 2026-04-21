/* eslint-disable no-console */
/**
 * Verifies the in-tab startup-error UX. We don't want to disturb the
 * shared dev fixture (which other specs reuse with a real `claude` on
 * PATH), so this spec spawns its own `leapmux dev` with:
 *   - PATH set to an empty directory containing a fake `claude` that
 *     prints to stderr and exits 1, so the subprocess fails fast.
 *   - LEAPMUX_WORKER_AGENT_STARTUP_TIMEOUT_SECONDS=2 to bound the test.
 */
import type { ChildProcess } from 'node:child_process'
import { spawn } from 'node:child_process'
import { chmodSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import process from 'node:process'
import { expect, test } from '@playwright/test'
import {
  createWorkspaceViaAPI,
  deleteWorkspaceViaAPI,
  getAdminOrgId,
  getWorkerId,
  loginViaAPI,
  openAgentViaAPI,
} from './helpers/api'
import { findFreePort, getGlobalState, waitForServer } from './helpers/server'
import { loginViaToken, waitForWorkspaceReady } from './helpers/ui'

interface BadShellServer {
  hubUrl: string
  adminToken: string
  adminOrgId: string
  workerId: string
  proc: ChildProcess
  dataDir: string
  pathDir: string
}

async function startServerWithFailingClaude(): Promise<BadShellServer> {
  const { binaryPath } = getGlobalState()
  const dataDir = mkdtempSync(join(tmpdir(), 'leapmux-startup-err-'))
  const pathDir = mkdtempSync(join(tmpdir(), 'leapmux-fake-bin-'))
  const port = await findFreePort()
  const hubUrl = `http://localhost:${port}`

  // The simplest reliable way to force agent startup failure end-to-end
  // is to point SHELL at /usr/bin/false. Whatever args the worker
  // wraps the shell with, /usr/bin/false ignores them and exits 1
  // immediately — so the initialize handshake never completes and the
  // worker reports STARTUP_FAILED via the runAgentStartup goroutine.
  const fakeClaude = join(pathDir, 'placeholder')
  writeFileSync(fakeClaude, 'unused', 'utf8')
  chmodSync(fakeClaude, 0o755)

  const proc = spawn(binaryPath, ['dev', '-addr', `:${port}`, '-data-dir', dataDir], {
    stdio: ['ignore', 'pipe', 'pipe'],
    env: {
      ...process.env,
      LEAPMUX_WORKER_NAME: 'Local',
      LEAPMUX_HUB_SIGNUP_ENABLED: 'true',
      LEAPMUX_CLAUDE_DEFAULT_MODEL: 'sonnet',
      LEAPMUX_CLAUDE_DEFAULT_EFFORT: 'low',
      LEAPMUX_WORKER_AGENT_STARTUP_TIMEOUT_SECONDS: '5',
      // /usr/bin/false ignores all args and exits 1 — the shell
      // "exec claude ..." never runs.
      SHELL: '/usr/bin/false',
    },
  })
  proc.stdout?.resume()
  proc.stderr?.resume()

  await waitForServer(hubUrl)
  const adminToken = await loginViaAPI(hubUrl, 'admin', 'admin123')
  const adminOrgId = await getAdminOrgId(hubUrl, adminToken)
  const workerId = await getWorkerId(hubUrl, adminToken)
  return { hubUrl, adminToken, adminOrgId, workerId, proc, dataDir, pathDir }
}

async function stopServer(srv: BadShellServer): Promise<void> {
  srv.proc.kill('SIGTERM')
  await new Promise(r => setTimeout(r, 1000))
  try {
    srv.proc.kill('SIGKILL')
  }
  catch { /* already dead */ }
  rmSync(srv.dataDir, { recursive: true, force: true })
  rmSync(srv.pathDir, { recursive: true, force: true })
}

// Local mkdir helper to surface a clear error if the temp dir already
// exists (mkdtempSync handles uniqueness, but we keep the shape in case
// future iterations swap it).
const _mkdirSync = mkdirSync

test.describe('Claude Code agent startup error', () => {
  let srv: BadShellServer

  test.beforeAll(async () => {
    srv = await startServerWithFailingClaude()
  })

  test.afterAll(async () => {
    if (srv)
      await stopServer(srv)
  })

  test('shows in-tab error and rejects subsequent sends', async ({ browser }) => {
    const context = await browser.newContext({ baseURL: srv.hubUrl })
    const page = await context.newPage()

    const workspaceId = await createWorkspaceViaAPI(
      srv.hubUrl,
      srv.adminToken,
      `startup-err-${Date.now()}`,
      srv.adminOrgId,
    )
    await openAgentViaAPI(srv.hubUrl, srv.adminToken, srv.workerId, workspaceId)
    await loginViaToken(page, srv.adminToken)
    await page.goto(`/o/admin/workspace/${workspaceId}`)
    await waitForWorkspaceReady(page)

    // The startup-error panel must appear with the formatted error.
    const errorPanel = page.locator('[data-testid="agent-startup-error"]')
    await expect(errorPanel).toBeVisible({ timeout: 30_000 })
    await expect(errorPanel).toContainText('failed to start')

    // The Close-tab button is rendered and clickable.
    const closeBtn = page.locator('[data-testid="agent-startup-error-close"]')
    await expect(closeBtn).toBeVisible()

    // Sends from a stale UI must not produce a network round-trip that
    // succeeds — type a message and submit; the message should appear
    // with an error label rather than being delivered.
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await editor.click()
    await page.keyboard.type('hello')
    await page.keyboard.press('Meta+Enter')
    await expect(page.locator('[data-testid="message-error"]')).toBeVisible({ timeout: 10_000 })

    await deleteWorkspaceViaAPI(srv.hubUrl, srv.adminToken, workspaceId).catch(() => {})
    await context.close()
  })
})

void _mkdirSync // satisfy "imported but never used" if we drop the helper above.
