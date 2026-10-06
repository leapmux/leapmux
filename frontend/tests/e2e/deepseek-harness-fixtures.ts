import type { AgentWorkspace, ProviderAgent } from './helpers/workspace'
import type { CliSkipFixture } from './provider-fixture-factory'
import { DEEPSEEK_HARNESS_MODE, DEEPSEEK_HARNESS_OPTION, DEEPSEEK_HARNESS_PERMISSION_PRESET } from '../../src/generated/contracts/deepseek-harness-protocol'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { test as base, expect } from './fixtures'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { createTestDirectory } from './helpers/runDirectory'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { createGitRepo } from './helpers/worktree'
import { cliSkipFixture } from './provider-fixture-factory'

export const DEEPSEEK_HARNESS_E2E_SKIP_REASON: string | null = missingBinaryReason('dsh', 'DeepSeek Harness E2E requires the native dsh executable.')

/** A working directory that is the root of a git repository of its own. */
export function createDeepseekHarnessWorkingDir(): string {
  return createGitRepo(createTestDirectory('deepseek-harness-e2e-wd-'), 'repo')
}

/** How a DeepSeek Harness agent opens. */
export const DEEPSEEK_HARNESS_AGENT: ProviderAgent = { provider: AgentProvider.DEEPSEEK_HARNESS, prefix: 'deepseek-harness-e2e', workingDir: createDeepseekHarnessWorkingDir }

/** The option values of an Act agent with one permission preset. */
function actWith(permissions: string) {
  return { optionValues: { permissionMode: DEEPSEEK_HARNESS_MODE.Act, [DEEPSEEK_HARNESS_OPTION.Permissions]: permissions } }
}

export const deepseekHarnessTest = base.extend<CliSkipFixture & {
  /** An Act agent with full access, which runs every tool call at once. */
  authenticatedDeepseekHarnessWorkspace: AgentWorkspace
  /** An Act agent with workspace write access, which asks before a command escalates out of its sandbox. */
  askingDeepseekHarnessWorkspace: AgentWorkspace
}>({
  cliSkip: cliSkipFixture(DEEPSEEK_HARNESS_E2E_SKIP_REASON),
  authenticatedDeepseekHarnessWorkspace: authenticatedAgentWorkspace({ ...DEEPSEEK_HARNESS_AGENT, openOptions: actWith(DEEPSEEK_HARNESS_PERMISSION_PRESET.DangerFullAccess) }),
  askingDeepseekHarnessWorkspace: authenticatedAgentWorkspace({ ...DEEPSEEK_HARNESS_AGENT, prefix: 'deepseek-harness-e2e-ask', openOptions: actWith(DEEPSEEK_HARNESS_PERMISSION_PRESET.WorkspaceWrite) }),
})

export { expect }
