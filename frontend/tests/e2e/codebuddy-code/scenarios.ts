import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import type { NativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import { randomUUID } from 'node:crypto'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { nativeTextStep } from '../helpers/nativeScenario'
import { resolveNativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import { spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { openRunningNativeChild } from '../helpers/runningChildProof'
import { uniqueMarker } from '../helpers/shellArguments'

/** Supply this provider's native answer and housekeeping turns to neutral browser scenarios. */
export async function nativeContext(context: Omit<ManagedNativeScenarioContext, 'provider' | 'textStep'>): Promise<ManagedNativeScenarioContext> {
  return { ...context, provider: AgentProvider.CODEBUDDY }
}

/** Select the actual isolated executable and hold only its native runtime invocation. */
export function nativeLaunch(context: ManagedNativeScenarioContext): NativeStartupLaunch {
  return resolveNativeStartupLaunch(context.leapmuxServer.agentEnv, { binaryName: 'codebuddy', holdWhen: ['-p'] })
}

/** Open this provider's actual child task and hold its native final answer. */
export async function runningChild(context: ManagedNativeScenarioContext) {
  const task = `${uniqueMarker('NATIVECHILDTASK')} report one word.`
  const spawn = spawnSubagentToolCall(context.provider, 'native-held-child', { description: 'Native held child', prompt: context.modelScript.prompt(task) })
  return openRunningNativeChild(context, {
    gate: `native-child-${randomUUID()}`,
    childMatcher: { user: task },
    childFinalStep: nativeTextStep(context, 'NATIVECHILDCOMPLETE'),
    spawn,
    parentSteps: [{ toolCalls: [spawn] }, nativeTextStep(context, 'The native parent completed.')],
    rules: [],
  })
}
