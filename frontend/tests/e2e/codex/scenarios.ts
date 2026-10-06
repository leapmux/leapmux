import type { ManagedNativeScenarioContext, NativeContextFixtures } from '../helpers/nativeScenario'
import type { NativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import type { ProviderAgent } from '../helpers/workspace'
import { CODEX_EXECUTABLE } from '../../../src/generated/contracts/codex-protocol'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseConversationContext } from '../helpers/nativeConversation'
import { managedNativeContext } from '../helpers/nativeScenario'
import { resolveNativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import { readCodexExecResult } from './execResult'

/** How a Codex agent opens. */
export const CODEX_AGENT: ProviderAgent = { provider: AgentProvider.CODEX, prefix: 'codex-e2e' }

/** Build the scenario context of Codex. Its native protocol needs no field beyond the provider. */
export async function nativeContext(fixtures: NativeContextFixtures): Promise<ManagedNativeScenarioContext> {
  return managedNativeContext(fixtures, CODEX_AGENT)
}

/**
 * Select the actual isolated executable and hold only its native runtime invocation. The Worker probes each name of
 * the contract, so the executable can carry a later name while the wrapper takes the preferred one.
 */
export function nativeLaunch(context: ManagedNativeScenarioContext): NativeStartupLaunch {
  return resolveNativeStartupLaunch(
    context.leapmuxServer.agentEnv,
    { binaryName: CODEX_EXECUTABLE.Preferred, holdWhen: ['app-server'] },
    Object.values(CODEX_EXECUTABLE),
  )
}

/** The related proof of a missing-setting cell: a later native request carries the earlier prompt and answer. */
export async function relatedNativeProof(context: ManagedNativeScenarioContext): Promise<void> {
  await exerciseConversationContext(context)
}

/**
 * Give a Codex context the reader of an exec cell result, for a scenario whose tool calls run through Codex's code
 * cell. Other Codex results have another shape, so `nativeContext` keeps the default reader.
 */
export function codexExecContext(context: ManagedNativeScenarioContext): ManagedNativeScenarioContext {
  return { ...context, readToolResult: readCodexExecResult }
}
