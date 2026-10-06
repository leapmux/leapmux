import type { ManagedNativeScenarioContext, NativeContextFixtures } from '../helpers/nativeScenario'
import type { NativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import type { HeldNativeChild } from '../helpers/runningChildProof'
import { randomUUID } from 'node:crypto'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { nativeTextStep } from '../helpers/nativeScenario'
import { resolveNativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import { JUNIE_ANSWER_TOOL, junieAnswerToolCall, junieSubagentSubmitToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { openRunningNativeChild } from '../helpers/runningChildProof'
import { uniqueMarker } from '../helpers/shellArguments'
import { exerciseCapabilityProbe } from '../helpers/unsupportedConfiguration'
import { junieModelTurns } from './modelTurns'

/**
 * Build the scenario context of Junie, with every field that its native protocol needs.
 * Junie answers through its `answer` tool, so each answer is that call, and a turn with only that call has no tool row.
 * The Junie test object registers the housekeeping rules of every test (`junie-fixtures.ts`), so the context registers none.
 */
export async function nativeContext(fixtures: NativeContextFixtures): Promise<ManagedNativeScenarioContext> {
  return {
    ...fixtures,
    provider: AgentProvider.JUNIE,
    textStep: (text: string) => ({ toolCalls: [junieAnswerToolCall(`junie-answer-${randomUUID()}`, text)] }),
    answerToolNames: [JUNIE_ANSWER_TOOL],
    // Junie compresses the prior exchange into its previous_issue row, and the reader splits it back into turns.
    readConversationTurns: junieModelTurns,
  }
}

/** Select the actual isolated executable and hold only its native runtime invocation. */
export function nativeLaunch(context: ManagedNativeScenarioContext): NativeStartupLaunch {
  return resolveNativeStartupLaunch(context.leapmuxServer.agentEnv, { binaryName: 'junie', holdWhen: ['--acp=true'] })
}

/** Open this provider's actual child task and hold its native final answer. */
export async function runningChild(context: ManagedNativeScenarioContext, options: { allowExistingRows?: boolean } = {}): Promise<HeldNativeChild> {
  const task = `${uniqueMarker('NATIVECHILDTASK')} report one word.`
  const description = `Native held child ${randomUUID()}`
  const agentType = 'leapmux-e2e-child'
  const spawn = spawnSubagentToolCall(context.provider, `native-held-child-${randomUUID()}`, { description, prompt: context.modelScript.prompt(task), agentType })
  return openRunningNativeChild(context, {
    gate: `native-child-${randomUUID()}`,
    child: {
      matcher: { system: 'You are the LeapMux test subagent', body: task },
      finalStep: { toolCalls: [junieSubagentSubmitToolCall(`native-child-submit-${randomUUID()}`, '### Summary\n- NATIVECHILDCOMPLETE\n### Changes\n- No files changed.\n### Verification\n- Answered the scripted task.')] },
    },
    spawn,
    allowExistingRows: options.allowExistingRows ?? false,
    rowText: agentType,
    parentSteps: [{ toolCalls: [spawn] }, nativeTextStep(context, 'The native parent completed.')],
  })
}

/** The related proof of a missing-setting cell: the native model answers one marked prompt. */
export async function relatedNativeProof(context: ManagedNativeScenarioContext): Promise<void> {
  await exerciseCapabilityProbe(context)
}
