import type { WorkspaceFixture } from './helpers/workspace'
/**
 * ZCode fixtures use the shared agent workspace lifetime.
 * The skip check requires a launcher or bundled script and a usable provider configuration.
 * These checks match the worker's launch requirements.
 */
import { execFileSync } from 'node:child_process'
import { existsSync, readFileSync } from 'node:fs'
import { homedir } from 'node:os'
import process from 'node:process'
import { AgentProvider } from './acp-fixture-factory'
import { test as base, expect } from './fixtures'

import { loginViaToken, openWorkspace } from './helpers/ui'
import { withAgentWorkspace } from './helpers/workspace'
import { computeZCodeE2ESkipReason } from './zcode-install'

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
    await withAgentWorkspace(leapmuxServer, { provider: AgentProvider.ZCODE, prefix: 'zcode-e2e' }, use)
  },

  authenticatedZCodeWorkspace: async ({ page, zcodeWorkspace, leapmuxServer }, use) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await openWorkspace(page, zcodeWorkspace.workspaceId)

    await use(zcodeWorkspace)
  },
})

export { expect }
