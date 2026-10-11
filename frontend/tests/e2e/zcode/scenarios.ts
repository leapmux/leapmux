import type { ManagedNativeScenarioContext, NativeContextFixtures } from '../helpers/nativeScenario'
import type { ProviderAgent } from '../helpers/workspace'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { managedNativeContext } from '../helpers/nativeScenario'
import { gitRepositoryWorkingDir } from '../helpers/providerWorkingDir'
import { exerciseRelatedTodo } from '../helpers/relatedTodoProof'
import { applyPermissionPreset } from '../helpers/ui'

/**
 * How a ZCode agent opens. Its working directory is the root of a git repository of its own.
 *
 * ZCode reads the nearest AGENTS.md from its working directory up to the first directory that holds `.git`, and up to
 * the root of the file system when none does (`findInstructionFile` in the `zcode.cjs` of ZCode). No setting moves that
 * limit, so a repository of its own stops ZCode at the working directory, where `workspace-trust.spec.ts` puts its own
 * AGENTS.md.
 */
export const ZCODE_AGENT: ProviderAgent = { provider: AgentProvider.ZCODE, prefix: 'zcode-e2e', workingDir: gitRepositoryWorkingDir }

/** Build the scenario context of ZCode. Its native protocol needs no field beyond the provider. */
export async function nativeContext(fixtures: NativeContextFixtures): Promise<ManagedNativeScenarioContext> {
  return managedNativeContext(fixtures, ZCODE_AGENT)
}

/**
 * Let each native tool run with no permission request. ZCode asks before each native tool runs, so a scenario that runs
 * a tool and answers no request applies the bypass preset first.
 */
export async function bypassToolRequests(context: Pick<ManagedNativeScenarioContext, 'page'>): Promise<void> {
  await applyPermissionPreset(context.page, 'bypass')
}

/** The related proof of a missing-setting cell: a native to-do call fills the sidebar. */
export async function relatedNativeProof(context: ManagedNativeScenarioContext): Promise<void> {
  await exerciseRelatedTodo(context, { prepare: () => bypassToolRequests(context) })
}
