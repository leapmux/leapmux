import type { ManagedNativeScenarioContext, NativeContextFixtures } from '../helpers/nativeScenario'
import type { NativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import type { ProviderAgent } from '../helpers/workspace'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { managedNativeContext } from '../helpers/nativeScenario'
import { resolveNativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { gitRepositoryWorkingDir } from '../helpers/providerWorkingDir'

/**
 * How a Grok Build agent opens. Its working directory is the root of a git repository of its own.
 *
 * Grok records folder trust for the git repository around the working directory. It asks the client whether to trust a
 * repository that holds configuration of its own:
 *
 * - An `AGENTS.md`.
 * - An `.mcp.json`.
 * - Hooks.
 *
 * The run directory sits inside the LeapMux checkout, whose root holds such files, so every agent that opened there
 * raised the trust question before its first turn. A repository of its own holds none of them, so nothing in it calls
 * for trust:
 *
 * - Grok asks nothing.
 * - The workspace stays untrusted.
 * - None of the configuration of the checkout loads.
 *
 * `149-grok-settings-trust` asks the question on purpose.
 */
export const GROK_AGENT: ProviderAgent = { provider: AgentProvider.GROK_BUILD, prefix: 'grok-e2e', workingDir: gitRepositoryWorkingDir }

/** Build the scenario context of Grok Build. Its native protocol needs no field beyond the provider. */
export async function nativeContext(fixtures: NativeContextFixtures): Promise<ManagedNativeScenarioContext> {
  return managedNativeContext(fixtures, GROK_AGENT)
}

/** Select the actual isolated executable and hold only its native runtime invocation. */
export function nativeLaunch(context: ManagedNativeScenarioContext): NativeStartupLaunch {
  return resolveNativeStartupLaunch(context.leapmuxServer.agentEnv, { binaryName: 'grok', holdWhen: ['agent', 'stdio'], lazy: false })
}

/** The related proof of a missing-setting cell: a real native shell command runs, and its output returns. */
export async function relatedNativeProof(context: ManagedNativeScenarioContext): Promise<void> {
  await exerciseShellToolExecution(context, { includeFailure: false })
}
