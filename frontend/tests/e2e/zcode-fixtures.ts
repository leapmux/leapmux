/**
 * ZCode fixtures use the shared agent workspace lifetime.
 * The skip check requires a launcher or bundled script and a usable provider configuration.
 * These checks match the worker's launch requirements.
 */
import type { AgentWorkspace, ProviderAgent } from './helpers/workspace'
import type { CliSkipFixture, NativeFixture } from './provider-fixture-factory'
import { existsSync, readFileSync } from 'node:fs'
import { homedir } from 'node:os'
import process from 'node:process'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { test as base, expect } from './fixtures'
import { findBinary, unusableBinaryReason } from './helpers/binaryOnPath'
import { hubSpawnEnv } from './helpers/server'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { cliSkipFixture } from './provider-fixture-factory'
import { computeZCodeE2ESkipReason } from './zcode-install'
import { nativeContext } from './zcode/scenarios'

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

/** How a ZCode agent opens. */
export const ZCODE_AGENT: ProviderAgent = { provider: AgentProvider.ZCODE, prefix: 'zcode-e2e' }

export const zcodeTest = base.extend<CliSkipFixture & NativeFixture & {
  authenticatedZCodeWorkspace: AgentWorkspace
}>({
  cliSkip: cliSkipFixture(ZCODE_E2E_SKIP_REASON),
  authenticatedZCodeWorkspace: authenticatedAgentWorkspace(ZCODE_AGENT),
  native: async ({ page, modelScript, leapmuxServer, authenticatedZCodeWorkspace }, use) => {
    await use(await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedZCodeWorkspace.workspaceId }))
  },
})

export { expect }
