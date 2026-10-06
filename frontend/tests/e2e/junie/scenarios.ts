import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import type { NativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import { randomUUID } from 'node:crypto'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { nativeTextStep } from '../helpers/nativeScenario'
import { resolveNativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import { JUNIE_ANSWER_TOOL, junieAnswerToolCall, junieSubagentSubmitToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { openRunningNativeChild } from '../helpers/runningChildProof'
import { uniqueMarker } from '../helpers/shellArguments'

/** Supply this provider's native answer and housekeeping turns to neutral browser scenarios. */
export async function nativeContext(context: Omit<ManagedNativeScenarioContext, 'provider' | 'textStep' | 'answerToolNames'>): Promise<ManagedNativeScenarioContext> {
  await context.modelScript.rule(
    { name: 'junie-native-capability', when: { system: 'capability filter agent' }, respond: { text: '' } },
    { name: 'junie-native-title', when: { system: 'task description summarizer' }, respond: { text: 'Native scenario' } },
  )
  return {
    ...context,
    provider: AgentProvider.JUNIE,
    textStep: (text: string) => ({ toolCalls: [junieAnswerToolCall(`junie-answer-${randomUUID()}`, text)] }),
    answerToolNames: [JUNIE_ANSWER_TOOL],
  }
}

/** Select the actual isolated executable and hold only its native runtime invocation. */
export function nativeLaunch(context: ManagedNativeScenarioContext): NativeStartupLaunch {
  return resolveNativeStartupLaunch(context.leapmuxServer.agentEnv, { binaryName: 'junie', holdWhen: ['--acp=true'] })
}

/** Open this provider's actual child task and hold its native final answer. */
export async function runningChild(context: ManagedNativeScenarioContext, options: { allowExistingRows?: boolean } = {}) {
  const task = `${uniqueMarker('NATIVECHILDTASK')} report one word.`
  const description = `Native held child ${randomUUID()}`
  const agentType = 'leapmux-e2e-child'
  const spawn = spawnSubagentToolCall(context.provider, `native-held-child-${randomUUID()}`, { description, prompt: context.modelScript.prompt(task), agentType })
  return openRunningNativeChild(context, {
    gate: `native-child-${randomUUID()}`,
    childMatcher: { system: 'You are the LeapMux test subagent', body: task },
    childFinalStep: { toolCalls: [junieSubagentSubmitToolCall(`native-child-submit-${randomUUID()}`, '### Summary\n- NATIVECHILDCOMPLETE\n### Changes\n- No files changed.\n### Verification\n- Answered the scripted task.')] },
    spawn,
    allowExistingRows: options.allowExistingRows ?? false,
    rowText: agentType,
    parentSteps: [{ toolCalls: [spawn] }, nativeTextStep(context, 'The native parent completed.')],
    rules: [{ name: 'junie-native-task-summary', when: { system: 'You are a task summarizer' }, respond: { text: '<summary>The native child completed.</summary><title>Native child</title>' } }],
  })
}
