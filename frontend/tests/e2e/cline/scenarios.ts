import type { ManagedNativeScenarioContext, NativeContextFixtures } from '../helpers/nativeScenario'
import type { NativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import type { ProviderAgent } from '../helpers/workspace'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { managedNativeContext } from '../helpers/nativeScenario'
import { resolveNativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { gitRepositoryWorkingDir } from '../helpers/providerWorkingDir'

/**
 * How a Cline agent opens. Its working directory is the root of a git repository of its own.
 *
 * Cline reads these from the workspace it runs in:
 *
 * - Rules.
 * - Skills.
 * - Workflows.
 *
 * Cline takes the root of the git repository around the working directory as that workspace. The run directory sits
 * inside the LeapMux checkout, whose root holds an `AGENTS.md`. A repository of its own holds nothing that Cline reads.
 */
export const CLINE_AGENT: ProviderAgent = { provider: AgentProvider.CLINE, prefix: 'cline-e2e', workingDir: gitRepositoryWorkingDir }

/** Build the scenario context of Cline. Its native protocol needs no field beyond the provider. */
export async function nativeContext(fixtures: NativeContextFixtures): Promise<ManagedNativeScenarioContext> {
  return managedNativeContext(fixtures, CLINE_AGENT)
}

/** Select the actual isolated executable and hold only its native runtime invocation. */
export function nativeLaunch(context: ManagedNativeScenarioContext): NativeStartupLaunch {
  return resolveNativeStartupLaunch(context.leapmuxServer.agentEnv, { binaryName: 'cline', holdWhen: ['--no-connectors'], lazy: false })
}

/** The related proof of a missing-setting cell: a real native shell command runs, and its output returns. */
export async function relatedNativeProof(context: ManagedNativeScenarioContext): Promise<void> {
  await exerciseShellToolExecution(context, { includeFailure: false })
}
