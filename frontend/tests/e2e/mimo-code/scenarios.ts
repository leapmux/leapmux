import type { ManagedNativeScenarioContext, NativeContextFixtures } from '../helpers/nativeScenario'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseMiMoShellToolExecution } from './shellToolExecution'

/** Build the scenario context of MiMo Code. Its native protocol needs no field beyond the provider. */
export async function nativeContext(fixtures: NativeContextFixtures): Promise<ManagedNativeScenarioContext> {
  return { ...fixtures, provider: AgentProvider.MIMO_CODE }
}

/** The related proof of a missing-setting cell: a real native shell command runs, and its output returns. */
export async function relatedNativeProof(context: ManagedNativeScenarioContext): Promise<void> {
  await exerciseMiMoShellToolExecution(context, { includeFailure: false })
}
