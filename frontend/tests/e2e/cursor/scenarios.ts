import type { ManagedNativeScenarioContext, NativeContextFixtures } from '../helpers/nativeScenario'
import type { NativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { resolveNativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import { exerciseRelatedTodo } from '../helpers/relatedTodoProof'
import { cursorModelTurns } from './modelTurns'

/**
 * Build the scenario context of Cursor, with every field that its native protocol needs.
 * Cursor sends only the current prompt and a conversation ID, so the context reads the conversation that the service
 * holds.
 */
export async function nativeContext(fixtures: NativeContextFixtures): Promise<ManagedNativeScenarioContext> {
  return { ...fixtures, provider: AgentProvider.CURSOR, readConversationTurns: cursorModelTurns }
}

/** Select the actual isolated executable and hold only its native runtime invocation. */
export function nativeLaunch(context: ManagedNativeScenarioContext): NativeStartupLaunch {
  return resolveNativeStartupLaunch(context.leapmuxServer.agentEnv, { binaryName: 'cursor-agent', holdWhen: ['acp'] })
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

/** The related proof of a missing-setting cell: a native to-do call fills the sidebar. */
export async function relatedNativeProof(context: ManagedNativeScenarioContext): Promise<void> {
  await exerciseCursorRelatedTodo(context)
}
