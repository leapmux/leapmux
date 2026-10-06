import type { ManagedNativeScenarioContext, NativeContextFixtures } from '../helpers/nativeScenario'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { ampToolResultReader } from '../helpers/ampToolResult'

/**
 * Build the scenario context of Amp, with every field that its native protocol needs.
 * Amp gives each scripted tool call a native ID that the thread of the agent derives, so the context reads a tool
 * result through that thread.
 */
export async function nativeContext(fixtures: NativeContextFixtures): Promise<ManagedNativeScenarioContext> {
  const context: ManagedNativeScenarioContext = { ...fixtures, provider: AgentProvider.AMP }
  context.readToolResult = ampToolResultReader(context)
  return context
}
