import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import type { NativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import { randomUUID } from 'node:crypto'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { nativeTextStep } from '../helpers/nativeScenario'
import { resolveNativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import { spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { nativeChildRuleId, openRunningNativeChild } from '../helpers/runningChildProof'
import { LETTA_TITLE_RULE } from '../letta-fixtures'
import { registerLettaChildNoticeRule } from './childNoticeRule'

/** Supply this provider's native answer and housekeeping turns to neutral browser scenarios. */
export async function nativeContext(context: Omit<ManagedNativeScenarioContext, 'provider' | 'textStep'>): Promise<ManagedNativeScenarioContext> {
  await context.modelScript.rule({ ...LETTA_TITLE_RULE, name: 'letta-native-context-title' })
  return { ...context, provider: AgentProvider.LETTA }
}

/** Select the actual isolated executable and hold only its native runtime invocation. */
export function nativeLaunch(context: ManagedNativeScenarioContext): NativeStartupLaunch {
  return resolveNativeStartupLaunch(context.leapmuxServer.agentEnv, { binaryName: 'letta', holdWhen: ['server'] })
}

/** Open this provider's actual child task and hold its native final answer. */
export async function runningChild(context: ManagedNativeScenarioContext, options: { allowExistingRows?: boolean } = {}) {
  const task = `NATIVECHILDTASK${randomUUID().replaceAll('-', '')} report one word.`
  const description = `Native held child ${randomUUID()}`
  const spawn = spawnSubagentToolCall(context.provider, `native-held-child-${randomUUID()}`, { description, prompt: context.modelScript.prompt(task) })
  const gate = `native-child-${randomUUID()}`
  return openRunningNativeChild(context, {
    gate,
    childMatcher: { user: task },
    childFinalStep: nativeTextStep(context, 'NATIVECHILDCOMPLETE'),
    spawn,
    allowExistingRows: options.allowExistingRows ?? false,
    rowText: description,
    parentSteps: [{ toolCalls: [spawn] }, nativeTextStep(context, 'The native parent completed.')],
    beforeRelease: async () => {
      await registerLettaChildNoticeRule(context, { name: nativeChildRuleId(gate, 'letta-native-child-notice'), spawnCallId: spawn.id, description, report: 'NATIVECHILDCOMPLETE', reply: 'The native child completed.' })
    },
  })
}
