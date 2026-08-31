/**
 * ZCode-specific e2e test fixtures.
 *
 * ZCode is a native line-delimited JSON agent (not ACP, not JSON-RPC), so the
 * fixture mirrors the Pi/Codex pattern rather than the shared ACP factory.
 *
 * The skip reason asks the same questions the worker asks at launch: a `zcode` on
 * PATH, else the bundled `zcode.cjs`, and then a `~/.zcode/v2/config.json` that
 * carries a usable provider. `StartZCode` fails on any of them, so a machine that
 * misses one would fail every test before it reached the chat surface.
 */
import { execFileSync } from 'node:child_process'
import { existsSync, mkdtempSync, readFileSync } from 'node:fs'
import { homedir, tmpdir } from 'node:os'
import { join } from 'node:path'
import process from 'node:process'
import { AgentProvider } from './acp-fixture-factory'
import { test as base, expect } from './fixtures'
import {
  createWorkspaceViaAPI,
  deleteWorkspaceViaAPI,
  openAgentViaAPI,
} from './helpers/api'
import { loginViaToken, openWorkspace } from './helpers/ui'
import { computeZCodeE2ESkipReason } from './zcode-install'

interface WorkspaceFixture {
  workspaceId: string
}

function zcodeOnPath(): boolean {
  try {
    execFileSync('zcode', ['--help'], { encoding: 'utf-8', stdio: 'ignore' })
    return true
  }
  catch (err) {
    // ENOENT is "not installed". Any other failure (a launcher that exists and
    // rejects --help, a permission error) still means a zcode is on PATH.
    return (err as NodeJS.ErrnoException).code !== 'ENOENT'
  }
}

export const ZCODE_E2E_SKIP_REASON: string | null = computeZCodeE2ESkipReason({
  scriptOverride: process.env.LEAPMUX_ZCODE_SCRIPT,
  scriptExists: existsSync,
  launcherOnPath: zcodeOnPath(),
  platform: process.platform,
  home: homedir(),
  env: process.env,
  readConfig: path => (existsSync(path) ? readFileSync(path, 'utf-8') : null),
})

export const zcodeTest = base.extend<{
  zcodeWorkspace: WorkspaceFixture
  authenticatedZCodeWorkspace: WorkspaceFixture
}>({
  zcodeWorkspace: async ({ leapmuxServer }, use) => {
    const { hubUrl, adminToken, workerId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(
      hubUrl,
      adminToken,
      `zcode-e2e-${Date.now()}-${Math.random().toString(36).slice(2, 7)}`,
    )
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, mkdtempSync(join(tmpdir(), 'zcode-e2e-wd-')), {
      agentProvider: AgentProvider.ZCODE,
    })
    await use({ workspaceId })

    try {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId)
    }
    catch { /* best effort */ }
  },

  authenticatedZCodeWorkspace: async ({ page, zcodeWorkspace, leapmuxServer }, use) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await openWorkspace(page, zcodeWorkspace.workspaceId)

    await use(zcodeWorkspace)
  },
})

export { expect }
