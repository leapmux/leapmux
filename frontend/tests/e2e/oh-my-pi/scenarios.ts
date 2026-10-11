import type { ManagedNativeScenarioContext, NativeContextFixtures } from '../helpers/nativeScenario'
import type { NativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import type { ProviderAgent } from '../helpers/workspace'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { managedNativeContext } from '../helpers/nativeScenario'
import { resolveNativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { gitRepositoryWorkingDir } from '../helpers/providerWorkingDir'

/**
 * How an Oh My Pi agent opens. Its working directory is the root of a git repository of its own.
 *
 * omp reads AGENTS.md, CLAUDE.md, the nearest `.omp/` and each `.agent/` and `.agents/` from its working directory up
 * to the first directory that holds `.git`, and up to the root of the file system when none does. Its setting
 * `disabledProviders` turns a discovery off in every directory, the working directory too, and the native discovery
 * also loads the project extensions of `workspace-trust.spec.ts`. A repository of its own stops omp at the working
 * directory. The one discovery that a repository does not stop, `.clinerules`, is off in the environment of omp
 * (`../helpers/ohMyPiEnvironment.ts`).
 */
export const OH_MY_PI_AGENT: ProviderAgent = { provider: AgentProvider.OH_MY_PI, prefix: 'omp-e2e', workingDir: gitRepositoryWorkingDir }

/** Build the scenario context of Oh My Pi. Its native protocol needs no field beyond the provider. */
export async function nativeContext(fixtures: NativeContextFixtures): Promise<ManagedNativeScenarioContext> {
  return managedNativeContext(fixtures, OH_MY_PI_AGENT)
}

/** Select the actual isolated executable and hold only its native runtime invocation. */
export function nativeLaunch(context: ManagedNativeScenarioContext): NativeStartupLaunch {
  return resolveNativeStartupLaunch(context.leapmuxServer.agentEnv, { binaryName: 'omp', holdWhen: ['rpc-ui'], lazy: false })
}

/** The related proof of a missing-setting cell: a real native shell command runs, and its output returns. */
export async function relatedNativeProof(context: ManagedNativeScenarioContext): Promise<void> {
  await exerciseShellToolExecution(context, { includeFailure: false })
}
