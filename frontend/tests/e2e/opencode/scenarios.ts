import type { ManagedNativeScenarioContext, NativeContextFixtures } from '../helpers/nativeScenario'
import type { NativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import type { ProviderAgent } from '../helpers/workspace'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { managedNativeContext } from '../helpers/nativeScenario'
import { resolveNativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import { exerciseRelatedTodo } from '../helpers/relatedTodoProof'

/** How an OpenCode agent opens. */
export const OPENCODE_AGENT: ProviderAgent = { provider: AgentProvider.OPENCODE, prefix: 'opencode-e2e' }

/** Build the scenario context of OpenCode. Its native protocol needs no field beyond the provider. */
export async function nativeContext(fixtures: NativeContextFixtures): Promise<ManagedNativeScenarioContext> {
  return managedNativeContext(fixtures, OPENCODE_AGENT)
}

/** Select the actual isolated executable and hold only its native runtime invocation. */
export function nativeLaunch(context: ManagedNativeScenarioContext): NativeStartupLaunch {
  return resolveNativeStartupLaunch(context.leapmuxServer.agentEnv, { binaryName: 'opencode', holdWhen: ['acp'] })
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
