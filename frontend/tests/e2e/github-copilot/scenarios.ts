import type { ManagedNativeScenarioContext, NativeContextFixtures } from '../helpers/nativeScenario'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseRelatedTodo } from '../helpers/relatedTodoProof'
import { applyPermissionPreset } from '../helpers/ui'

/** Build the scenario context of GitHub Copilot. Its native protocol needs no field beyond the provider. */
export async function nativeContext(fixtures: NativeContextFixtures): Promise<ManagedNativeScenarioContext> {
  return { ...fixtures, provider: AgentProvider.GITHUB_COPILOT }
}

/**
 * The related proof of a missing-setting cell: a native to-do call fills the sidebar.
 * GitHub Copilot asks before each native tool runs, so the proof applies the bypass preset first.
 */
export async function relatedNativeProof(context: ManagedNativeScenarioContext): Promise<void> {
  await exerciseRelatedTodo(context, { prepare: () => applyPermissionPreset(context.page, 'bypass') })
}
