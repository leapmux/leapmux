import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import type { NativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import type { NativeChildScriptContext } from '../helpers/runningChildProof'
import { randomUUID } from 'node:crypto'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { DROID_TITLE_RULE } from '../droid-fixtures'
import { nativeTextStep } from '../helpers/nativeScenario'
import { resolveNativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import { spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { openRunningNativeChild } from '../helpers/runningChildProof'
import { droidChildNoticeRule } from './childNotice'
import { readDroidToolResult } from './toolResult'

/** Supply this provider's native answer and housekeeping turns to neutral browser scenarios. */
export async function nativeContext(context: Omit<ManagedNativeScenarioContext, 'provider' | 'textStep'>): Promise<ManagedNativeScenarioContext> {
  await context.modelScript.rule({ ...DROID_TITLE_RULE, name: 'droid-native-context-title' })
  return { ...context, provider: AgentProvider.DROID, readToolResult: readDroidToolResult }
}

/** Select the actual isolated executable and hold only its native runtime invocation. */
export function nativeLaunch(context: ManagedNativeScenarioContext): NativeStartupLaunch {
  return resolveNativeStartupLaunch(context.leapmuxServer.agentEnv, { binaryName: 'droid', holdWhen: ['exec'] })
}

/** Open this provider's actual child task and hold its native final answer. */
export async function runningChild(context: ManagedNativeScenarioContext, options: { allowExistingRows?: boolean } = {}) {
  return openRunningNativeChild(context, runningChildOptions({ provider: context.provider, prompt: text => context.modelScript.prompt(text), textStep: text => nativeTextStep(context, text) }, options))
}

/** Build the actual native child script without browser operations. */
export function runningChildOptions(context: NativeChildScriptContext, options: { allowExistingRows?: boolean } = {}) {
  const task = `NATIVECHILDTASK${randomUUID().replaceAll('-', '')} report one word.`
  const description = `Native held child ${randomUUID()}`
  const spawn = spawnSubagentToolCall(context.provider, `native-held-child-${randomUUID()}`, { description, prompt: context.prompt(task), background: true })
  return {
    gate: `native-child-${randomUUID()}`,
    childMatcher: { system: 'READ-ONLY exploration', body: task },
    childFinalStep: context.textStep('NATIVECHILDCOMPLETE'),
    spawn,
    allowExistingRows: options.allowExistingRows ?? false,
    rowText: description,
    parentSteps: [{ toolCalls: [spawn] }, context.textStep('The native parent completed.')],
    rules: [droidChildNoticeRule(description, { text: 'The native child completed.' })],
  }
}
