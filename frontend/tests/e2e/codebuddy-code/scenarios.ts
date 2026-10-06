import type { ManagedNativeScenarioContext, NativeContextFixtures } from '../helpers/nativeScenario'
import type { NativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import type { HeldNativeChild } from '../helpers/runningChildProof'
import type { ProviderAgent } from '../helpers/workspace'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { managedNativeContext } from '../helpers/nativeScenario'
import { resolveNativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import { heldChildIdentity, heldChildOptions, nativeChildScriptContext, openRunningNativeChild } from '../helpers/runningChildProof'
import { exerciseCapabilityProbe } from '../helpers/unsupportedConfiguration'
import { gitRepositoryWorkingDir } from '../helpers/worktree'

/** How a CodeBuddy agent opens. Its working directory is the root of a git repository of its own. */
export const CODEBUDDY_AGENT: ProviderAgent = { provider: AgentProvider.CODEBUDDY, prefix: 'codebuddy-e2e', workingDir: gitRepositoryWorkingDir }

/** Build the scenario context of CodeBuddy Code. Its native protocol needs no field beyond the provider. */
export async function nativeContext(fixtures: NativeContextFixtures): Promise<ManagedNativeScenarioContext> {
  return managedNativeContext(fixtures, CODEBUDDY_AGENT)
}

/** Select the actual isolated executable and hold only its native runtime invocation. */
export function nativeLaunch(context: ManagedNativeScenarioContext): NativeStartupLaunch {
  return resolveNativeStartupLaunch(context.leapmuxServer.agentEnv, { binaryName: 'codebuddy', holdWhen: ['-p'] })
}

/** Open this provider's actual child task and hold its native final answer. CodeBuddy Code needs only the defaults. */
export async function runningChild(context: ManagedNativeScenarioContext): Promise<HeldNativeChild> {
  const script = nativeChildScriptContext(context)
  return openRunningNativeChild(context, heldChildOptions(script, heldChildIdentity(script)))
}

/** The related proof of a missing-setting cell: the native model answers one marked prompt. */
export async function relatedNativeProof(context: ManagedNativeScenarioContext): Promise<void> {
  await exerciseCapabilityProbe(context)
}
