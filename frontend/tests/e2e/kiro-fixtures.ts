/**
 * Kiro E2E fixtures, on the shared agent workspace lifetime.
 *
 * Kiro talks to its own service, which `helpers/kiroSurface.ts` answers on the mock
 * endpoint, and `helpers/mockAgentEnvironment.ts` points every Kiro setting at that
 * endpoint under an isolated HOME. No test reaches a real Kiro account.
 *
 * The skip check looks for the `kiro-cli-chat` binary without running it: the check
 * runs with the developer's own HOME, and Kiro reads its configuration directory on
 * every start. See `helpers/binaryOnPath.ts`.
 */
import type { AgentWorkspace, ProviderAgent } from './helpers/workspace'
import type { CliSkipFixture, NativeFixture } from './provider-fixture-factory'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { test as base, expect } from './fixtures'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { createTestDirectory } from './helpers/runDirectory'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { createGitRepo } from './helpers/worktree'
import { nativeContext } from './kiro/scenarios'
import { cliSkipFixture } from './provider-fixture-factory'

/**
 * A working directory that is the root of a git repository of its own.
 *
 * Kiro reads steering documents, agents and hooks from the workspace it runs in. The
 * run directory sits inside the LeapMux checkout, whose root holds such files, and a
 * repository of its own holds none of them.
 */
export function createKiroWorkingDir(): string {
  return createGitRepo(createTestDirectory('kiro-e2e-wd-'), 'repo')
}

export const KIRO_E2E_SKIP_REASON: string | null = missingBinaryReason('kiro-cli-chat', 'Kiro E2E requires the kiro-cli-chat CLI on PATH')

/** How a Kiro agent opens. */
export const KIRO_AGENT: ProviderAgent = { provider: AgentProvider.KIRO, prefix: 'kiro-e2e', workingDir: createKiroWorkingDir }

export const kiroTest = base.extend<CliSkipFixture & NativeFixture & {
  authenticatedKiroWorkspace: AgentWorkspace
}>({
  cliSkip: cliSkipFixture(KIRO_E2E_SKIP_REASON),
  authenticatedKiroWorkspace: authenticatedAgentWorkspace(KIRO_AGENT),
  native: async ({ page, modelScript, leapmuxServer, authenticatedKiroWorkspace }, use) => {
    await use(await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedKiroWorkspace.workspaceId }))
  },
})

export { expect }
