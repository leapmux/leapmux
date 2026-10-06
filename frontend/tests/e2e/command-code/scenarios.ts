import type { ManagedNativeScenarioContext, NativeContextFixtures } from '../helpers/nativeScenario'
import type { HeldNativeChild } from '../helpers/runningChildProof'
import type { ProviderAgent } from '../helpers/workspace'
import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { currentNativeAgent, managedNativeContext } from '../helpers/nativeScenario'
import { resolveNativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { readToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { HELD_NATIVE_CHILD_DESCRIPTION, openRunningNativeChild } from '../helpers/runningChildProof'
import { uniqueMarker } from '../helpers/shellArguments'
import { gitRepositoryWorkingDir } from '../helpers/worktree'

/** How a Command Code agent opens. Its working directory is the root of a git repository of its own. */
export const COMMAND_CODE_AGENT: ProviderAgent = { provider: AgentProvider.COMMAND_CODE, prefix: 'command-code-e2e', workingDir: gitRepositoryWorkingDir }

/** Build the scenario context of Command Code. Its native protocol needs no field beyond the provider. */
export async function nativeContext(fixtures: NativeContextFixtures): Promise<ManagedNativeScenarioContext> {
  return managedNativeContext(fixtures, COMMAND_CODE_AGENT)
}

export function nativeLaunch(context: ManagedNativeScenarioContext) {
  return resolveNativeStartupLaunch(context.leapmuxServer.agentEnv, { binaryName: 'command-code', holdWhen: ['--rpc'] })
}

/** Hold the native final reply after an actual child file read. */
export async function runningChild(context: ManagedNativeScenarioContext, options: { allowExistingRows?: boolean } = {}): Promise<HeldNativeChild> {
  const parent = await currentNativeAgent(context)
  const marker = uniqueMarker()
  const task = `COMMANDCODECHILD${marker} read the supplied file and report one word.`
  const path = join(parent.workingDir, `native-child-${marker}.txt`)
  writeFileSync(path, `NATIVE_CHILD_FILE${marker}\n`)
  const spawn = spawnSubagentToolCall(context.provider, `spawn-${marker}`, { description: HELD_NATIVE_CHILD_DESCRIPTION, prompt: context.modelScript.prompt(task) })
  return openRunningNativeChild(context, {
    gate: `native-child-${marker}`,
    child: {
      matcher: { user: task },
      tool: readToolCall(context.provider, `child-read-${marker}`, path),
      finalStep: { text: `NATIVE_CHILD_REPORT${marker}` },
    },
    spawn,
    parentSteps: [{ toolCalls: [spawn] }, { text: 'The native parent completed.' }],
    ...(options.allowExistingRows === undefined ? {} : { allowExistingRows: options.allowExistingRows }),
  })
}

/** The related proof of a missing-setting cell: a real native shell command runs, and its output returns. */
export async function relatedNativeProof(context: ManagedNativeScenarioContext): Promise<void> {
  await exerciseShellToolExecution(context, { includeFailure: false })
}
