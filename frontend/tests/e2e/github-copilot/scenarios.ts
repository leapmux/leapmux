import type { ManagedNativeScenarioContext, NativeContextFixtures } from '../helpers/nativeScenario'
import type { NativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import type { ProviderAgent } from '../helpers/workspace'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { managedNativeContext } from '../helpers/nativeScenario'
import { resolveNativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import { exerciseRelatedTodo } from '../helpers/relatedTodoProof'
import { applyPermissionPreset } from '../helpers/ui'

/** How a Copilot agent opens. */
export const COPILOT_AGENT: ProviderAgent = { provider: AgentProvider.GITHUB_COPILOT, prefix: 'copilot-e2e' }

/** Build the scenario context of GitHub Copilot. Its native protocol needs no field beyond the provider. */
export async function nativeContext(fixtures: NativeContextFixtures): Promise<ManagedNativeScenarioContext> {
  return managedNativeContext(fixtures, COPILOT_AGENT)
}

/** Select the actual isolated executable and hold only its native runtime invocation. */
export function nativeLaunch(context: ManagedNativeScenarioContext): NativeStartupLaunch {
  return resolveNativeStartupLaunch(context.leapmuxServer.agentEnv, { binaryName: 'copilot', holdWhen: ['--server', '--stdio'] })
}

/**
 * The related proof of a missing-setting cell: a native to-do call fills the sidebar.
 * GitHub Copilot asks before each native tool runs, so the proof applies the bypass preset first.
 */
export async function relatedNativeProof(context: ManagedNativeScenarioContext): Promise<void> {
  await exerciseRelatedTodo(context, { prepare: () => applyPermissionPreset(context.page, 'bypass') })
}
