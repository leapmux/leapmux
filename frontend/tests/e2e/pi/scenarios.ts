import type { ManagedNativeScenarioContext, NativeContextFixtures } from '../helpers/nativeScenario'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { piTodoToolCall } from '../helpers/providerToolCalls'
import { exerciseRelatedTodo, RELATED_TODO_CALL_ID, RELATED_TODO_ITEM } from '../helpers/relatedTodoProof'

/** Build the scenario context of Pi. Its native protocol needs no field beyond the provider. */
export async function nativeContext(fixtures: NativeContextFixtures): Promise<ManagedNativeScenarioContext> {
  return { ...fixtures, provider: AgentProvider.PI }
}

/**
 * The related proof of a missing-setting cell: a native to-do call fills the sidebar.
 * Pi's vocabulary has no update-todos call, so the proof gives Pi's own to-do call for the default item.
 */
export async function relatedNativeProof(context: ManagedNativeScenarioContext): Promise<void> {
  await exerciseRelatedTodo(context, { toolCall: piTodoToolCall(RELATED_TODO_CALL_ID, { action: 'create', subject: RELATED_TODO_ITEM }) })
}
