import type { ManagedNativeScenarioContext, NativeContextFixtures } from '../helpers/nativeScenario'
import type { NativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import type { ProviderAgent } from '../helpers/workspace'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { managedNativeContext } from '../helpers/nativeScenario'
import { resolveNativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import { exerciseRelatedTodo } from '../helpers/relatedTodoProof'
import { applyPermissionPreset } from '../helpers/ui'

/** How a Goose agent opens. */
export const GOOSE_AGENT: ProviderAgent = { provider: AgentProvider.GOOSE, prefix: 'goose-e2e' }

/** Build the scenario context of Goose. Its native protocol needs no field beyond the provider. */
export async function nativeContext(fixtures: NativeContextFixtures): Promise<ManagedNativeScenarioContext> {
  return managedNativeContext(fixtures, GOOSE_AGENT)
}

/** Select the actual isolated executable and hold only its native runtime invocation. */
export function nativeLaunch(context: ManagedNativeScenarioContext): NativeStartupLaunch {
  return resolveNativeStartupLaunch(context.leapmuxServer.agentEnv, { binaryName: 'goose', holdWhen: ['acp'] })
}

/**
 * Apply the bypass preset, so a native tool runs with no permission request.
 * Goose asks before each native tool runs. A scenario that runs a tool and answers no request calls this
 * function first, so this fact of the provider lives here alone.
 */
export async function bypassToolRequests(context: Pick<ManagedNativeScenarioContext, 'page'>): Promise<void> {
  await applyPermissionPreset(context.page, 'bypass')
}

/** The related proof of a missing-setting cell: a native to-do call fills the sidebar. */
export async function relatedNativeProof(context: ManagedNativeScenarioContext): Promise<void> {
  await exerciseRelatedTodo(context, { prepare: () => bypassToolRequests(context) })
}
