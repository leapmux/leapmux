import type { ManagedNativeScenarioContext, NativeContextFixtures } from '../helpers/nativeScenario'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseRelatedTodo } from '../helpers/relatedTodoProof'

/** Build the scenario context of Cursor. Its native protocol needs no field beyond the provider. */
export async function nativeContext(fixtures: NativeContextFixtures): Promise<ManagedNativeScenarioContext> {
  return { ...fixtures, provider: AgentProvider.CURSOR }
}

/**
 * Create the related to-do item through Cursor's native to-do tool.
 *
 * One Cursor turn is one Run exchange: the service streams the tool calls and the answer text in one response
 * (`helpers/cursorSurface.ts`). The tool call and the answer therefore form one scripted step.
 */
export function exerciseCursorRelatedTodo(context: ManagedNativeScenarioContext): Promise<void> {
  return exerciseRelatedTodo(context, { singleRequest: true })
}
