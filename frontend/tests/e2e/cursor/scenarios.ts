import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { exerciseRelatedTodo } from '../helpers/relatedTodoProof'

/**
 * Create the related to-do item through Cursor's native to-do tool.
 *
 * One Cursor turn is one Run exchange: the service streams the tool calls and the answer text in one response
 * (`helpers/cursorSurface.ts`). The tool call and the answer therefore form one scripted step.
 */
export function exerciseCursorRelatedTodo(context: ManagedNativeScenarioContext): Promise<void> {
  return exerciseRelatedTodo(context, { singleRequest: true })
}
