/**
 * Grok Build e2e test fixtures.
 */
import type { AgentWorkspace, ProviderAgent } from './helpers/workspace'
import type { CliSkipFixture, NativeFixture } from './provider-fixture-factory'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { test as base, expect } from './fixtures'
import { nativeContext } from './grok-build/scenarios'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { gitRepositoryWorkingDir } from './helpers/worktree'
import { cliSkipFixture } from './provider-fixture-factory'

export const GROK_E2E_SKIP_REASON: string | null = missingBinaryReason('grok', 'Grok Build E2E requires a grok CLI on PATH')

/**
 * How a Grok Build agent opens. Its working directory is the root of a git repository of its own.
 *
 * Grok keys folder trust by the git repository around the working directory, and
 * it asks the client whether to trust a repository that holds configuration of
 * its own -- an `AGENTS.md`, an `.mcp.json`, hooks. The run directory sits inside
 * the LeapMux checkout, whose root holds such files, so every agent opened there
 * raised the trust question before its first turn. A repository of its own holds
 * none, so nothing in it calls for trust: Grok asks nothing, the workspace stays
 * untrusted, and none of the checkout's configuration loads.
 * `149-grok-settings-trust` asks the question on purpose.
 */
export const GROK_AGENT: ProviderAgent = { provider: AgentProvider.GROK_BUILD, prefix: 'grok-e2e', workingDir: gitRepositoryWorkingDir }

export const grokTest = base.extend<CliSkipFixture & NativeFixture & {
  authenticatedGrokWorkspace: AgentWorkspace
}>({
  cliSkip: cliSkipFixture(GROK_E2E_SKIP_REASON),
  authenticatedGrokWorkspace: authenticatedAgentWorkspace(GROK_AGENT),
  native: async ({ page, modelScript, leapmuxServer, authenticatedGrokWorkspace }, use) => {
    await use(await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedGrokWorkspace.workspaceId }))
  },
})

export { expect }
