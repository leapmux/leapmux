import type { ManagedNativeScenarioContext, NativeContextFixtures } from '../helpers/nativeScenario'
import type { NativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import type { ProviderAgent } from '../helpers/workspace'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { managedNativeContext } from '../helpers/nativeScenario'
import { resolveNativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { ampToolResultReader } from './toolResult'

/** How an Amp agent opens. */
export const AMP_AGENT: ProviderAgent = { provider: AgentProvider.AMP, prefix: 'amp-e2e' }

/**
 * Build the scenario context of Amp, with every field that its native protocol needs.
 * Amp gives each scripted tool call a native ID that the thread of the agent derives, so the context reads a tool
 * result through that thread.
 */
export async function nativeContext(fixtures: NativeContextFixtures): Promise<ManagedNativeScenarioContext> {
  const context = managedNativeContext(fixtures, AMP_AGENT)
  context.readToolResult = ampToolResultReader(context)
  return context
}

/** Select the actual isolated executable and hold only its native runtime invocation. */
export function nativeLaunch(context: ManagedNativeScenarioContext): NativeStartupLaunch {
  return resolveNativeStartupLaunch(context.leapmuxServer.agentEnv, { binaryName: 'amp', holdWhen: ['--execute'], lazy: true })
}

/** The related proof of a missing-setting cell: a real native shell command runs, and its output returns. */
export async function relatedNativeProof(context: ManagedNativeScenarioContext): Promise<void> {
  await exerciseShellToolExecution(context, { includeFailure: false })
}
