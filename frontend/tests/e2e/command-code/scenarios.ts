import type { ManagedNativeScenarioContext, NativeContextFixtures } from '../helpers/nativeScenario'
import type { HeldNativeChild } from '../helpers/runningChildProof'
import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { resolveNativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import { readToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { openRunningNativeChild } from '../helpers/runningChildProof'
import { uniqueMarker } from '../helpers/shellArguments'

/** Build the scenario context of Command Code. Its native protocol needs no field beyond the provider. */
export async function nativeContext(fixtures: NativeContextFixtures): Promise<ManagedNativeScenarioContext> {
  return { ...fixtures, provider: AgentProvider.COMMAND_CODE }
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
  const spawn = spawnSubagentToolCall(context.provider, `spawn-${marker}`, { description: 'Native held child', prompt: context.modelScript.prompt(task) })
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
