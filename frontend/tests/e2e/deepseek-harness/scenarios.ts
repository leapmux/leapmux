import type { ManagedNativeScenarioContext, NativeContextFixtures } from '../helpers/nativeScenario'
import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { currentNativeAgent, nativeAgentById } from '../helpers/nativeScenario'
import { readNativeSidebarSnapshot } from '../helpers/nativeSidebarSnapshot'
import { resolveNativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { readToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { HELD_NATIVE_CHILD_DESCRIPTION, openRunningNativeChild } from '../helpers/runningChildProof'
import { uniqueMarker } from '../helpers/shellArguments'
import { finishDeepseekHarnessChild, waitForDeepseekHarnessChildReport } from './childReportCompletion'
import { registerDeepseekHarnessChildReport } from './childReportRegistration'
import { deepseekHarnessModelContextText } from './modelContextText'

/**
 * Build the scenario context of DeepSeek Harness, with every field that its native protocol needs.
 * The generic model-context reader reads the JSON body, where a quote arrives escaped, so the context reads the text
 * of the native Anthropic Messages request.
 */
export async function nativeContext(fixtures: NativeContextFixtures): Promise<ManagedNativeScenarioContext> {
  return { ...fixtures, provider: AgentProvider.DEEPSEEK_HARNESS, readModelContext: deepseekHarnessModelContextText }
}

export function nativeLaunch(context: ManagedNativeScenarioContext) {
  return resolveNativeStartupLaunch(context.leapmuxServer.agentEnv, { binaryName: 'dsh', holdWhen: ['--profile', 'web'] })
}

/** Hold one continuable native child after its actual file read. */
export async function runningChild(context: ManagedNativeScenarioContext) {
  const parent = await currentNativeAgent(context)
  const marker = uniqueMarker()
  const task = `DEEPSEEKCHILD${marker} read the supplied file.`
  const path = join(parent.workingDir, `native-child-${marker}.txt`)
  writeFileSync(path, `NATIVE_CHILD_FILE${marker}\n`)
  const spawn = spawnSubagentToolCall(context.provider, `spawn-${marker}`, { description: HELD_NATIVE_CHILD_DESCRIPTION, prompt: context.modelScript.prompt(task), background: true })
  const readCallId = `child-read-${marker}`
  const child = await openRunningNativeChild(context, {
    gate: `native-child-${marker}`,
    // The final turn of the child carries the file marker of its Read result, and the final matcher selects that turn.
    child: {
      matcher: { user: task },
      tool: readToolCall(context.provider, readCallId, path),
      finalMatcher: { user: `NATIVE_CHILD_FILE${marker}` },
      finalStep: { text: `NATIVE_CHILD_REPORT${marker}` },
    },
    spawn,
    parentSteps: [{ toolCalls: [spawn] }, { text: 'The native parent completed.' }],
    beforeRelease: () => registerChildReports(context),
  })
  return {
    ...child,
    /** The call ID of the native Read that the child makes. */
    readCallId,
    finish: () => finishDeepseekHarnessChild(child.finish, () => waitForDeepseekHarnessChildReport(context, child.childId, child.parentId)),
  }
}

/** Register exact completion reports before a held native child can finish. */
export async function registerChildReports(context: ManagedNativeScenarioContext): Promise<void> {
  const snapshot = await readNativeSidebarSnapshot(context)
  for (const task of snapshot.backgroundTasks) {
    if (!task.childAgentId)
      continue
    const child = await nativeAgentById(context, task.childAgentId)
    if (!child?.agentSessionId)
      throw new Error('The DeepSeek Harness child report has no stored native Session identity.')
    await registerDeepseekHarnessChildReport(context.modelScript, child.agentSessionId)
  }
}

/** The related proof of a missing-setting cell: a real native shell command runs, and its output returns. */
export async function relatedNativeProof(context: ManagedNativeScenarioContext): Promise<void> {
  await exerciseShellToolExecution(context, { includeFailure: false })
}
