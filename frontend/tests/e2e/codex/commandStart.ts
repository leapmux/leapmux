import type { AgentChatMessage } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { CODEX_ITEM } from '../../../src/generated/contracts/codex-protocol'
import { isObject } from '../../../src/lib/jsonPick'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent } from '../helpers/nativeScenario'

/**
 * Whether the Worker stored the start of a native Codex command item.
 *
 * Codex states the start in its `item/started` notification, and the Worker stores
 * that notification as Codex sent it. Codex attaches to a command's output at that
 * moment, so the command's later output is the output that Codex streams live.
 */
export function codexCommandStarted(messages: readonly AgentChatMessage[], sessionId: string): boolean {
  if (!sessionId)
    throw new Error('The native Codex command start requires its session ID.')
  return messages.some((message) => {
    if (message.agentSessionId !== sessionId || message.spanType !== CODEX_ITEM.CommandExecution)
      return false
    const body = nativeMessageBody(message)
    return isObject(body) && body.threadId === sessionId && isObject(body.item)
      && body.item.type === CODEX_ITEM.CommandExecution && body.item.status === 'inProgress'
  })
}

/** Resolve when the Worker holds the start of a Codex command item of the active agent. */
export async function waitForCodexCommandStart(context: Pick<ManagedNativeScenarioContext, 'page' | 'leapmuxServer'>): Promise<void> {
  const agent = await currentNativeAgent(context)
  await expect.poll(async () => {
    const snapshot = await readNativeMessageSnapshot(context, agent.id)
    return codexCommandStarted(snapshot.messages, snapshot.agentSessionId)
  }).toBe(true)
}
