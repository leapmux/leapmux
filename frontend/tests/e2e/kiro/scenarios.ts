import type { ManagedNativeScenarioContext, NativeContextFixtures } from '../helpers/nativeScenario'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { kiroToolResult } from '../helpers/kiroToolResult'

/**
 * Build the scenario context of Kiro, with every field that its native protocol needs.
 * Kiro returns a tool result in the conversation state of its own service request, so the context reads it there.
 */
export async function nativeContext(fixtures: NativeContextFixtures): Promise<ManagedNativeScenarioContext> {
  return { ...fixtures, provider: AgentProvider.KIRO, readToolResult: kiroToolResult }
}
