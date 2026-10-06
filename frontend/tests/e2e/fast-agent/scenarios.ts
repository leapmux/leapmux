import type { MockModelToolCall } from '../helpers/mockModelScript'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import type { NativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import type { NativeChildScriptContext } from '../helpers/runningChildProof'
import { randomUUID } from 'node:crypto'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { nativeTextStep } from '../helpers/nativeScenario'
import { resolveNativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import { spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { openRunningNativeChild } from '../helpers/runningChildProof'
import { uniqueMarker } from '../helpers/shellArguments'

/** Supply this provider's native answer and housekeeping turns to neutral browser scenarios. */
export async function nativeContext(context: Omit<ManagedNativeScenarioContext, 'provider' | 'textStep'>): Promise<ManagedNativeScenarioContext> {
  return { ...context, provider: AgentProvider.FAST_AGENT }
}

/** Select the actual isolated executable and hold only its native runtime invocation. */
export function nativeLaunch(context: ManagedNativeScenarioContext): NativeStartupLaunch {
  return resolveNativeStartupLaunch(context.leapmuxServer.agentEnv, { binaryName: 'fast-agent', holdWhen: ['acp'] })
}

/** Open this provider's actual child task and hold its native final answer. */
export async function runningChild(context: ManagedNativeScenarioContext, options: { allowExistingRows?: boolean, childTool?: MockModelToolCall } = {}) {
  return openRunningNativeChild(context, runningChildOptions({ provider: context.provider, prompt: text => context.modelScript.prompt(text), textStep: text => nativeTextStep(context, text) }, options))
}

/** Build the actual native child script without browser operations. */
export function runningChildOptions(context: NativeChildScriptContext, options: { allowExistingRows?: boolean, childTool?: MockModelToolCall } = {}) {
  const report = uniqueMarker('NATIVECHILDCOMPLETE')
  const task = `${uniqueMarker('NATIVECHILDTASK')} report exactly ${report}.`
  const description = `Native held child ${randomUUID().slice(0, 8)}`
  const spawn = spawnSubagentToolCall(context.provider, `native-held-child-${randomUUID()}`, { description, prompt: context.prompt(task) })
  return {
    gate: `native-child-${randomUUID()}`,
    childMatcher: { lastMessage: { role: 'user' as const, text: task } },
    ...(options.childTool ? { childTool: options.childTool } : {}),
    childFinalStep: context.textStep(report),
    spawn,
    allowExistingRows: options.allowExistingRows ?? false,
    rowText: description,
    parentSteps: [{ toolCalls: [spawn] }, context.textStep('The native parent completed.')],
    rules: [],
  }
}
