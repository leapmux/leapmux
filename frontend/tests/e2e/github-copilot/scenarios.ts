import type { ManagedNativeScenarioContext, NativeContextFixtures } from '../helpers/nativeScenario'
import type { NativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import type { ProviderAgent } from '../helpers/workspace'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { managedNativeContext } from '../helpers/nativeScenario'
import { resolveNativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import { gitRepositoryWorkingDir } from '../helpers/providerWorkingDir'
import { exerciseRelatedTodo } from '../helpers/relatedTodoProof'
import { applyPermissionPreset } from '../helpers/ui'

/**
 * How a Copilot agent opens. Its working directory is the root of a git repository of its own.
 *
 * Copilot CLI reads its custom instructions (AGENTS.md, CLAUDE.md, GEMINI.md and the `.github/` files) from each
 * directory between its working directory and the root of its git repository, and from directories above that when no
 * repository holds the working directory. Its one switch, `--no-custom-instructions`, also turns off the AGENTS.md of
 * the working directory that `workspace-trust.spec.ts` proves. A repository of its own stops Copilot at the working
 * directory.
 */
export const COPILOT_AGENT: ProviderAgent = { provider: AgentProvider.GITHUB_COPILOT, prefix: 'copilot-e2e', workingDir: gitRepositoryWorkingDir }

/** Build the scenario context of GitHub Copilot. Its native protocol needs no field beyond the provider. */
export async function nativeContext(fixtures: NativeContextFixtures): Promise<ManagedNativeScenarioContext> {
  return managedNativeContext(fixtures, COPILOT_AGENT)
}

/** Select the actual isolated executable and hold only its native runtime invocation. */
export function nativeLaunch(context: ManagedNativeScenarioContext): NativeStartupLaunch {
  return resolveNativeStartupLaunch(context.leapmuxServer.agentEnv, { binaryName: 'copilot', holdWhen: ['--server', '--stdio'] })
}

/**
 * Apply the bypass preset, so a native tool runs with no permission request.
 * GitHub Copilot asks before each native tool runs. A scenario that runs a tool and answers no request calls this
 * function first, so this fact of the provider lives here alone.
 */
export async function bypassToolRequests(context: Pick<ManagedNativeScenarioContext, 'page'>): Promise<void> {
  await applyPermissionPreset(context.page, 'bypass')
}

/** The related proof of a missing-setting cell: a native to-do call fills the sidebar. */
export async function relatedNativeProof(context: ManagedNativeScenarioContext): Promise<void> {
  await exerciseRelatedTodo(context, { prepare: () => bypassToolRequests(context) })
}
