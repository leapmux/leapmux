import type { ManagedNativeScenarioContext, NativeContextFixtures } from '../helpers/nativeScenario'
import type { NativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import type { ProviderAgent } from '../helpers/workspace'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { managedNativeContext } from '../helpers/nativeScenario'
import { resolveNativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { gitRepositoryWorkingDir } from '../helpers/worktree'
import { kiroModelTurns } from './modelTurns'
import { kiroToolResult } from './toolResult'

/**
 * How a Kiro agent opens. Its working directory is the root of a git repository of its own.
 *
 * Kiro reads these from the workspace it runs in:
 *
 * - Steering documents.
 * - Agents.
 * - Hooks.
 *
 * The run directory sits inside the LeapMux checkout, whose root holds such files. A repository of its own holds none
 * of them.
 */
export const KIRO_AGENT: ProviderAgent = { provider: AgentProvider.KIRO, prefix: 'kiro-e2e', workingDir: gitRepositoryWorkingDir }

/**
 * Build the scenario context of Kiro, with every field that its native protocol needs.
 * Kiro returns a tool result in the conversation state of its own service request, so the context reads it there.
 * The same request states the conversation in a shape of its own, so the context reads the turns there also.
 */
export async function nativeContext(fixtures: NativeContextFixtures): Promise<ManagedNativeScenarioContext> {
  return managedNativeContext(fixtures, KIRO_AGENT, { readToolResult: kiroToolResult, readConversationTurns: kiroModelTurns })
}

/** Select the actual isolated executable and hold only its native runtime invocation. */
export function nativeLaunch(context: ManagedNativeScenarioContext): NativeStartupLaunch {
  return resolveNativeStartupLaunch(context.leapmuxServer.agentEnv, { binaryName: 'kiro-cli-chat', holdWhen: ['acp'], lazy: false })
}

/** The related proof of a missing-setting cell: a real native shell command runs, and its output returns. */
export async function relatedNativeProof(context: ManagedNativeScenarioContext): Promise<void> {
  await exerciseShellToolExecution(context, { includeFailure: false })
}
