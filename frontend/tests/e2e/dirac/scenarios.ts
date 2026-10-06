import type { MockModelToolCall } from '../helpers/mockModelScript'
import type { ManagedNativeScenarioContext, NativeContextFixtures } from '../helpers/nativeScenario'
import type { NativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import type { HeldNativeChild } from '../helpers/runningChildProof'
import { randomUUID } from 'node:crypto'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { nativeTextStep } from '../helpers/nativeScenario'
import { resolveNativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import { diracRespondToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { openRunningNativeChild } from '../helpers/runningChildProof'
import { uniqueMarker } from '../helpers/shellArguments'
import { diracChildResultRule } from './childResult'

/**
 * Build the scenario context of Dirac, with every field that its native protocol needs.
 * A Dirac turn ends only when the model calls `respond` with `operation: "complete"`, so each answer is that call.
 */
export async function nativeContext(fixtures: NativeContextFixtures): Promise<ManagedNativeScenarioContext> {
  return { ...fixtures, provider: AgentProvider.DIRAC, textStep: (text: string) => ({ toolCalls: [diracRespondToolCall(`dirac-complete-${randomUUID()}`, 'complete', text)] }) }
}

/** Select the actual isolated executable and hold only its native runtime invocation. */
export function nativeLaunch(context: ManagedNativeScenarioContext): NativeStartupLaunch {
  return resolveNativeStartupLaunch(context.leapmuxServer.agentEnv, { binaryName: 'dirac', holdWhen: ['--acp'] })
}

/** Open this provider's actual child task and hold its native final answer. */
export async function runningChild(context: ManagedNativeScenarioContext, options: { allowExistingRows?: boolean, childTool?: MockModelToolCall } = {}): Promise<HeldNativeChild> {
  const taskMarker = uniqueMarker('NATIVECHILDTASK')
  const task = `${taskMarker} report one word.`
  const description = `Native held child ${randomUUID()}`
  const spawn = spawnSubagentToolCall(context.provider, 'native-held-child', { description, prompt: context.modelScript.prompt(task) })
  const matcher = { body: task }
  const finalStep = nativeTextStep(context, 'NATIVECHILDCOMPLETE')
  return openRunningNativeChild(context, {
    gate: `native-child-${randomUUID()}`,
    child: options.childTool ? { matcher, tool: options.childTool, finalStep } : { matcher, finalStep },
    spawn,
    allowExistingRows: options.allowExistingRows ?? false,
    rowText: description,
    parentSteps: [{ toolCalls: [spawn] }],
    rules: [diracChildResultRule(taskMarker, nativeTextStep(context, 'The native parent completed.'))],
  })
}
