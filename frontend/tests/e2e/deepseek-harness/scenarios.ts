import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { randomUUID } from 'node:crypto'
import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { currentNativeAgent, nativeAgentById } from '../helpers/nativeScenario'
import { readNativeSidebarSnapshot } from '../helpers/nativeSidebarSnapshot'
import { resolveNativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import { readToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { openRunningNativeChild } from '../helpers/runningChildProof'
import { finishDeepseekHarnessChild, waitForDeepseekHarnessChildReport } from './childReportCompletion'
import { registerDeepseekHarnessChildReport } from './childReportRegistration'
import { deepseekHarnessModelContextText } from './modelContextText'

export function nativeContext(context: Omit<ManagedNativeScenarioContext, 'provider'>): ManagedNativeScenarioContext {
  return { ...context, provider: AgentProvider.DEEPSEEK_HARNESS, readModelContext: deepseekHarnessModelContextText }
}

export function nativeLaunch(context: ManagedNativeScenarioContext) {
  return resolveNativeStartupLaunch(context.leapmuxServer.agentEnv, { binaryName: 'dsh', holdWhen: ['--profile', 'web'] })
}

/** Hold one continuable native child after its actual file read. */
export async function runningChild(context: ManagedNativeScenarioContext) {
  const parent = await currentNativeAgent(context)
  const marker = randomUUID().replaceAll('-', '')
  const task = `DEEPSEEKCHILD${marker} read the supplied file.`
  const path = join(parent.workingDir, `native-child-${marker}.txt`)
  writeFileSync(path, `NATIVE_CHILD_FILE${marker}\n`)
  const spawn = spawnSubagentToolCall(context.provider, `spawn-${marker}`, { description: 'Native held child', prompt: context.modelScript.prompt(task), background: true })
  const child = await openRunningNativeChild(context, {
    gate: `native-child-${marker}`,
    childMatcher: { user: task },
    childFinalMatcher: { user: `NATIVE_CHILD_FILE${marker}` },
    childTool: readToolCall(context.provider, `child-read-${marker}`, path),
    childFinalStep: { text: `NATIVE_CHILD_REPORT${marker}` },
    spawn,
    parentSteps: [{ toolCalls: [spawn] }, { text: 'The native parent completed.' }],
    beforeRelease: () => registerChildReports(context),
  })
  return {
    ...child,
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
