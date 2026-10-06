import type { ManagedNativeScenarioContext, NativeContextFixtures } from '../helpers/nativeScenario'
import type { ProviderAgent } from '../helpers/workspace'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { managedNativeContext } from '../helpers/nativeScenario'
import { exerciseRelatedTodo } from '../helpers/relatedTodoProof'
import { applyPermissionPreset } from '../helpers/ui'

/** How a ZCode agent opens. */
export const ZCODE_AGENT: ProviderAgent = { provider: AgentProvider.ZCODE, prefix: 'zcode-e2e' }

/** Build the scenario context of ZCode. Its native protocol needs no field beyond the provider. */
export async function nativeContext(fixtures: NativeContextFixtures): Promise<ManagedNativeScenarioContext> {
  return managedNativeContext(fixtures, ZCODE_AGENT)
}

/**
 * The related proof of a missing-setting cell: a native to-do call fills the sidebar.
 * ZCode asks before each native tool runs, so the proof applies the bypass preset first.
 */
export async function relatedNativeProof(context: ManagedNativeScenarioContext): Promise<void> {
  await exerciseRelatedTodo(context, { prepare: () => applyPermissionPreset(context.page, 'bypass') })
}
