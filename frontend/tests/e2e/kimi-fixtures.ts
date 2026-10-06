/**
 * Kimi Code end-to-end fixtures.
 *
 * Every spec reaches the mock model endpoint through the configuration that
 * `helpers/mockAgentEnvironment.ts` writes to `KIMI_CODE_HOME`, never a Kimi
 * account.
 */
import type { AgentWorkspace } from './helpers/workspace'
import type { CliSkipFixture, NativeFixture } from './provider-fixture-factory'
import { test as base } from './fixtures'
import { lookupBinary, versionOutput } from './helpers/binaryOnPath'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { KIMI_AGENT, nativeContext } from './kimi-code/scenarios'
import { computeKimiE2ESkipReason, KIMI_MISSING_REASON } from './kimi-install'
import { cliSkipFixture } from './provider-fixture-factory'

// The check finds the file first without running it, which also refuses a mise
// shim (see `helpers/binaryOnPath.ts`). Only then does it run that file's
// `--version`, because the version decides between Kimi Code and the legacy
// kimi-cli. A kimi that exists and fails `--version` is not one that the worker
// can start either, so its empty answer skips too.
const KIMI = lookupBinary('kimi', KIMI_MISSING_REASON)
export const KIMI_E2E_SKIP_REASON: string | null = KIMI.path === null
  ? KIMI.skipReason
  : computeKimiE2ESkipReason(versionOutput(KIMI.path) ?? '')

export const kimiTest = base.extend<CliSkipFixture & NativeFixture & {
  authenticatedKimiWorkspace: AgentWorkspace
}>({
  cliSkip: cliSkipFixture(KIMI_E2E_SKIP_REASON),
  authenticatedKimiWorkspace: authenticatedAgentWorkspace(KIMI_AGENT),
  native: async ({ page, modelScript, leapmuxServer, authenticatedKimiWorkspace }, use) => {
    await use(await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedKimiWorkspace.workspaceId }))
  },
})
