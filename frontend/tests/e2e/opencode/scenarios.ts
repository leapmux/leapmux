import type { ManagedNativeScenarioContext, NativeContextFixtures } from '../helpers/nativeScenario'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseRelatedTodo } from '../helpers/relatedTodoProof'

/** Build the scenario context of OpenCode. Its native protocol needs no field beyond the provider. */
export async function nativeContext(fixtures: NativeContextFixtures): Promise<ManagedNativeScenarioContext> {
  return { ...fixtures, provider: AgentProvider.OPENCODE }
}

/**
 * The line by which OpenCode states the path of an instruction file that it loaded in the instructions of a
 * request. Kilo, which builds on OpenCode, writes the same line.
 */
export function opencodeInstructionSource(path: string): string {
  return `Instructions from: ${path}`
}

/** The related proof of a missing-setting cell: a native to-do call fills the sidebar. */
export async function relatedNativeProof(context: ManagedNativeScenarioContext): Promise<void> {
  await exerciseRelatedTodo(context)
}
