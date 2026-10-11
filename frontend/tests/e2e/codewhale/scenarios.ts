import type { ManagedNativeScenarioContext, NativeContextFixtures } from '../helpers/nativeScenario'
import type { NativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import type { ProviderAgent } from '../helpers/workspace'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { managedNativeContext } from '../helpers/nativeScenario'
import { resolveNativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { gitRepositoryWorkingDir } from '../helpers/providerWorkingDir'
import { codewhaleToolRowIdResolver } from './toolCallIdentity'

/**
 * How a Codewhale agent opens. Its working directory is the root of a git repository of its own.
 *
 * Codewhale reads the nearest `.codewhale/constitution.json` from its working directory up to the root of its git
 * repository, and up to the root of the file system when no repository holds the working directory
 * (`load_repo_constitution_block`). Its project instructions run from the root of the repository down to the working
 * directory. No setting turns either off, so a repository of its own stops Codewhale at the working directory.
 */
export const CODEWHALE_AGENT: ProviderAgent = { provider: AgentProvider.CODEWHALE, prefix: 'codewhale-e2e', workingDir: gitRepositoryWorkingDir }

/** Build the scenario context of Codewhale. Its tool rows key by the runtime's own call id, so the context resolves them. */
export async function nativeContext(fixtures: NativeContextFixtures): Promise<ManagedNativeScenarioContext> {
  return managedNativeContext(fixtures, CODEWHALE_AGENT, { resolveToolRowId: codewhaleToolRowIdResolver(fixtures) })
}

/** Select the actual isolated executable and hold only its native runtime invocation. */
export function nativeLaunch(context: ManagedNativeScenarioContext): NativeStartupLaunch {
  return resolveNativeStartupLaunch(context.leapmuxServer.agentEnv, { binaryName: 'codewhale', holdWhen: ['app-server'], lazy: false })
}

/** The related proof of a missing-setting cell: a real native shell command runs, and its output returns. */
export async function relatedNativeProof(context: ManagedNativeScenarioContext): Promise<void> {
  await exerciseShellToolExecution(context, { includeFailure: false })
}
