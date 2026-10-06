import type { ManagedNativeScenarioContext, NativeContextFixtures } from '../helpers/nativeScenario'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseConversationContext } from '../helpers/nativeConversation'

/** Build the scenario context of Claude Code. Its native protocol needs no field beyond the provider. */
export async function nativeContext(fixtures: NativeContextFixtures): Promise<ManagedNativeScenarioContext> {
  return { ...fixtures, provider: AgentProvider.CLAUDE_CODE }
}

/** The related proof of a missing-setting cell: a later native request carries the earlier prompt and answer. */
export async function relatedNativeProof(context: ManagedNativeScenarioContext): Promise<void> {
  await exerciseConversationContext(context)
}
