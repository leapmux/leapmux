import type { ManagedNativeScenarioContext, NativeContextFixtures } from '../helpers/nativeScenario'
import type { NativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import type { ProviderAgent } from '../helpers/workspace'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { managedNativeContext } from '../helpers/nativeScenario'
import { resolveNativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import { exerciseMiMoShellToolExecution } from './shellToolExecution'
import { mimoToolRowIdResolver } from './toolRowId'

/** How a MiMo Code agent opens. */
export const MIMO_AGENT: ProviderAgent = { provider: AgentProvider.MIMO_CODE, prefix: 'mimo-e2e' }

/** Build the MiMo context with its provider-owned mapping from model calls to stored native part rows. */
export async function nativeContext(fixtures: NativeContextFixtures): Promise<ManagedNativeScenarioContext> {
  return managedNativeContext(fixtures, MIMO_AGENT, { resolveToolRowId: mimoToolRowIdResolver(fixtures) })
}

/** Select the actual isolated executable and hold only its native runtime invocation. */
export function nativeLaunch(context: ManagedNativeScenarioContext): NativeStartupLaunch {
  return resolveNativeStartupLaunch(context.leapmuxServer.agentEnv, { binaryName: 'mimo', holdWhen: ['serve'], lazy: false })
}

/** The related proof of a missing-setting cell: a real native shell command runs, and its output returns. */
export async function relatedNativeProof(context: ManagedNativeScenarioContext): Promise<void> {
  await exerciseMiMoShellToolExecution(context, { includeFailure: false })
}
