import type { ManagedNativeScenarioContext, NativeContextFixtures } from '../helpers/nativeScenario'
import type { NativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import type { ProviderAgent } from '../helpers/workspace'
import { GOOSE_MODE } from '../../../src/generated/contracts/goose-protocol'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { expectNativeOptionValue, managedNativeContext } from '../helpers/nativeScenario'
import { resolveNativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import { goosePermissionJudgmentToolCall } from '../helpers/providerToolCalls'
import { exerciseRelatedTodo } from '../helpers/relatedTodoProof'
import { uniqueMarker } from '../helpers/shellArguments'
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

/**
 * Make Goose ask before each native tool, in the Smart Approve mode that a new session starts in.
 * Smart Approve asks Goose's permission-safety classifier about each tool call, as one more model request. The rule
 * answers that no call is read-only, so each call raises a permission request.
 */
export async function askBeforeEachTool(context: Pick<ManagedNativeScenarioContext, 'page' | 'leapmuxServer' | 'modelScript'>): Promise<void> {
  await expectNativeOptionValue(context, 'permissionMode', GOOSE_MODE.SmartApprove)
  await context.modelScript.rule({
    name: `goose-native-tool-judge-${uniqueMarker()}`,
    when: { system: 'permission-safety classifier' },
    respond: { toolCalls: [goosePermissionJudgmentToolCall('goose-tool-judge', [])] },
  })
}

/** The related proof of a missing-setting cell: a native to-do call fills the sidebar. */
export async function relatedNativeProof(context: ManagedNativeScenarioContext): Promise<void> {
  await exerciseRelatedTodo(context, { prepare: () => bypassToolRequests(context) })
}
