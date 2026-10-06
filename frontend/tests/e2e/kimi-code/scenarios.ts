import type { ManagedNativeScenarioContext, NativeContextFixtures } from '../helpers/nativeScenario'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { kimiModelContextText } from './modelContextText'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'

/**
 * Build the scenario context of Kimi Code, with every field that its native protocol needs.
 * The generic model-context reader reads the JSON body, where a quote arrives escaped, so the context reads the text
 * of each native message. That reader refuses a message whose content is not text.
 */
export async function nativeContext(fixtures: NativeContextFixtures): Promise<ManagedNativeScenarioContext> {
  return { ...fixtures, provider: AgentProvider.KIMI_CODE, readModelContext: kimiModelContextText }
}

/** The related proof of a missing-setting cell: a real native shell command runs, and its output returns. */
export async function relatedNativeProof(context: ManagedNativeScenarioContext): Promise<void> {
  await exerciseShellToolExecution(context, { includeFailure: false })
}
