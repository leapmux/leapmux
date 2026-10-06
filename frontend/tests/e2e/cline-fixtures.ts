/**
 * Cline E2E fixtures.
 *
 * The worker starts one private Cline hub for each agent and drives it over Cline's
 * hub WebSocket protocol. Cline calls the model through its `deepseek`
 * provider, which `helpers/mockAgentEnvironment.ts` points at the mock endpoint in
 * Cline's own settings under an isolated HOME. No test reaches a Cline account, a
 * real model, or the developer's own Cline hub.
 *
 * The skip check looks for the `cline` binary without running it: the check runs
 * with the developer's own HOME, and Cline writes into its data directory on every
 * start. See `helpers/binaryOnPath.ts`.
 */
import type { AgentWorkspace, ProviderAgent } from './helpers/workspace'
import type { CliSkipFixture } from './provider-fixture-factory'
import { CLINE_PERMISSION_MODE } from '../../src/generated/contracts/cline-protocol'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { test as base, expect } from './fixtures'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { createTestDirectory } from './helpers/runDirectory'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { createGitRepo } from './helpers/worktree'
import { cliSkipFixture } from './provider-fixture-factory'

export const CLINE_E2E_SKIP_REASON: string | null = missingBinaryReason('cline', 'Cline E2E requires the cline CLI on PATH (https://cline.bot/cli)')

/**
 * A working directory that is the root of a git repository of its own.
 *
 * Cline reads rules, skills and workflows from the workspace it runs in, and it
 * takes the root of the git repository around the working directory as that
 * workspace. The run directory sits inside the LeapMux checkout, whose root holds an
 * `AGENTS.md`, and a repository of its own holds nothing that Cline reads.
 */
export function createClineWorkingDir(): string {
  return createGitRepo(createTestDirectory('cline-e2e-wd-'), 'repo')
}

/** How a Cline agent opens. */
export const CLINE_AGENT: ProviderAgent = { provider: AgentProvider.CLINE, prefix: 'cline-e2e', workingDir: createClineWorkingDir }

/**
 * The agent opens in Auto-approve, which answers every tool call at once.
 *
 * LeapMux opens a new Cline session in Act, which raises a banner for each edit and
 * each command. A spec that tests what a tool DRAWS would otherwise answer a banner
 * before each call. The control-request spec opens its own workspace in Act.
 */
const AUTO_APPROVE = { optionValues: { permissionMode: CLINE_PERMISSION_MODE.AutoApprove } }

export const clineTest = base.extend<CliSkipFixture & {
  /** An agent in Auto-approve, which raises no banner for a tool call. */
  authenticatedClineWorkspace: AgentWorkspace
  /** An agent in LeapMux's default Act mode, which asks before each edit and command. */
  askingClineWorkspace: AgentWorkspace
  /** An agent in Plan mode, which offers the plan tool. */
  planningClineWorkspace: AgentWorkspace
}>({
  cliSkip: cliSkipFixture(CLINE_E2E_SKIP_REASON),
  authenticatedClineWorkspace: authenticatedAgentWorkspace({ ...CLINE_AGENT, openOptions: AUTO_APPROVE }),
  askingClineWorkspace: authenticatedAgentWorkspace({ ...CLINE_AGENT, prefix: 'cline-e2e-act' }),
  planningClineWorkspace: authenticatedAgentWorkspace({ ...CLINE_AGENT, prefix: 'cline-e2e-plan', openOptions: { optionValues: { permissionMode: CLINE_PERMISSION_MODE.Plan } } }),
})

export { expect }

/**
 * The tool names that one recorded model call OFFERED, read from its `tools`.
 *
 * A tool name can also appear in the conversation that the call carries, as an
 * earlier call of the tool, so a search of the whole body cannot tell what the
 * session offers now.
 */
export function offeredTools(body: unknown): string[] {
  const tools = (body as { tools?: { function?: { name?: string } }[] } | undefined)?.tools ?? []
  return tools.map(tool => tool.function?.name ?? '')
}
