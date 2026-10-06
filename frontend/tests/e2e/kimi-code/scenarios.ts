import type { ManagedNativeScenarioContext, NativeContextFixtures } from '../helpers/nativeScenario'
import type { NativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import type { ProviderAgent } from '../helpers/workspace'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { managedNativeContext } from '../helpers/nativeScenario'
import { resolveNativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { kimiModelContextText } from './modelContextText'

/** How a Kimi Code agent opens. */
export const KIMI_AGENT: ProviderAgent = { provider: AgentProvider.KIMI_CODE, prefix: 'kimi-e2e' }

/**
 * Build the scenario context of Kimi Code, with every field that its native protocol needs.
 * The generic model-context reader reads the JSON body, where a quote arrives escaped, so the context reads the text
 * of each native message. That reader refuses a message whose content is not text.
 */
export async function nativeContext(fixtures: NativeContextFixtures): Promise<ManagedNativeScenarioContext> {
  return managedNativeContext(fixtures, KIMI_AGENT, { readModelContext: kimiModelContextText })
}

/** Select the actual isolated executable and hold only its native runtime invocation. */
export function nativeLaunch(context: ManagedNativeScenarioContext): NativeStartupLaunch {
  return resolveNativeStartupLaunch(context.leapmuxServer.agentEnv, { binaryName: 'kimi', holdWhen: ['web'], lazy: false })
}

/** The related proof of a missing-setting cell: a real native shell command runs, and its output returns. */
export async function relatedNativeProof(context: ManagedNativeScenarioContext): Promise<void> {
  await exerciseShellToolExecution(context, { includeFailure: false })
}
