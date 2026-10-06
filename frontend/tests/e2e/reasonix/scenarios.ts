import type { ManagedNativeScenarioContext, NativeContextFixtures } from '../helpers/nativeScenario'
import type { NativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import type { ProviderAgent } from '../helpers/workspace'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { managedNativeContext } from '../helpers/nativeScenario'
import { resolveNativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import { exerciseRelatedTodo } from '../helpers/relatedTodoProof'
import { applyPermissionPreset } from '../helpers/ui'

/** How a Reasonix agent opens. */
export const REASONIX_AGENT: ProviderAgent = { provider: AgentProvider.REASONIX, prefix: 'reasonix-e2e' }

/** Build the scenario context of Reasonix. Its native protocol needs no field beyond the provider. */
export async function nativeContext(fixtures: NativeContextFixtures): Promise<ManagedNativeScenarioContext> {
  return managedNativeContext(fixtures, REASONIX_AGENT)
}

/** Select the actual isolated executable and hold only its native runtime invocation. */
export function nativeLaunch(context: ManagedNativeScenarioContext): NativeStartupLaunch {
  return resolveNativeStartupLaunch(context.leapmuxServer.agentEnv, { binaryName: 'reasonix', holdWhen: ['acp'] })
}

/**
 * The related proof of a missing-setting cell: a native to-do call fills the sidebar.
 * Reasonix asks before each native tool runs, so the proof applies the bypass preset first.
 */
export async function relatedNativeProof(context: ManagedNativeScenarioContext): Promise<void> {
  await exerciseRelatedTodo(context, { prepare: () => applyPermissionPreset(context.page, 'bypass') })
}
