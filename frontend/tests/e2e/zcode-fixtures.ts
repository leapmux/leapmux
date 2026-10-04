import type { WorkspaceFixture } from './helpers/workspace'
/**
 * ZCode fixtures use the shared agent workspace lifetime.
 * The skip check requires a launcher or bundled script and a usable provider configuration.
 * These checks match the worker's launch requirements.
 */
import { existsSync, readFileSync } from 'node:fs'
import { homedir } from 'node:os'
import process from 'node:process'
import { AgentProvider } from './acp-fixture-factory'
import { test as base, expect } from './fixtures'
import { findBinary, unusableBinaryReason } from './helpers/binaryOnPath'
import { hubSpawnEnv } from './helpers/server'

import { loginViaToken, openWorkspace } from './helpers/ui'
import { withAgentWorkspace } from './helpers/workspace'
import { computeZCodeE2ESkipReason } from './zcode-install'

// The launcher is found without running it, so the check runs nothing in the
// developer's own HOME (see `helpers/binaryOnPath.ts`).
const ZCODE_LAUNCHER = findBinary('zcode')

export const ZCODE_E2E_SKIP_REASON: string | null = computeZCodeE2ESkipReason({
  scriptOverride: process.env.LEAPMUX_ZCODE_SCRIPT,
  scriptExists: existsSync,
  launcherOnPath: ZCODE_LAUNCHER !== null,
  launcherUnusableReason: ZCODE_LAUNCHER === null ? null : unusableBinaryReason('zcode', ZCODE_LAUNCHER),
  platform: process.platform,
  home: homedir(),
  env: hubSpawnEnv(),
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
