import type { MockModelRequestRecord } from './mockModelScript'
import type { ManagedNativeScenarioContext, NativeScenarioContext } from './nativeScenario'
import { expect } from '@playwright/test'
import { compactionNoticeRow } from './compaction'
import { sendNativeAnswer } from './nativeConversation'
import { currentNativeAgent, nativeModelLastUserText, nativeScenarioModelContextText, nativeTextStep } from './nativeScenario'
import { uniqueMarker } from './shellArguments'
import { observeSettledReceipts, waitForIdleSoundReceipt } from './turnEndSound'
import { assistantBubbles, sendMessage, waitForAgentIdle } from './ui'

/** The provider callback proves actual compaction or native command refusal before the UI check. */
export async function expectNoCompactionNotice(
  context: ManagedNativeScenarioContext,
  options: { relatedProof: () => Promise<unknown> },
): Promise<void> {
  const agent = await currentNativeAgent(context)
  const after = await observeSettledReceipts(context.page)
  await options.relatedProof()
  await waitForIdleSoundReceipt(context.page, { agentId: agent.id, after })
  await expect(compactionNoticeRow(context.page)).toHaveCount(0)
  await context.page.reload()
  await waitForAgentIdle(context.page)
  await expect(compactionNoticeRow(context.page)).toHaveCount(0)
}

/** How a provider receives `/compact` as model text. Each field has a default. */
export interface CompactAsModelTextOptions {
  /**
   * Send the command with the scenario marker on its last line, so the mock routes the command request by the
   * command itself and not by the earlier turns. The last user text then holds the command and the marker.
   */
  markCommand?: boolean
  /** Read the last user text of a request of the provider. The default reads a generic model API request. */
  lastUserText?: (request: MockModelRequestRecord) => string
}

/** What {@link exerciseCompactAsModelText} sent and read. */
export interface CompactAsModelTextResult {
  /** The model request of the earlier turn. */
  first: MockModelRequestRecord
  /** The model request that carried the command. */
  request: MockModelRequestRecord
  /** The prompt of the earlier turn, without its scenario marker. */
  prompt: string
  /** The answer of the earlier turn. */
  answer: string
}

/**
 * Prove that `/compact` reaches the native model as ordinary text, for a provider that has no native compaction.
 *
 * One earlier turn puts a marked prompt and answer into the conversation. Then the command goes to the model, and its
 * request must hold the command in the last user text, and the earlier prompt and answer in the model context. The
 * reply reaches the transcript, which draws no compaction notice. Every request index comes from the queue, so an
 * earlier turn of the test changes nothing.
 */
export async function exerciseCompactAsModelText(context: NativeScenarioContext, options: CompactAsModelTextOptions = {}): Promise<CompactAsModelTextResult> {
  const { page, modelScript } = context
  const marker = uniqueMarker('COMPACTTEXT')
  const prompt = `Keep PROMPT${marker} in the context before the compact command.`
  const answer = `The earlier context is present: ANSWER${marker}.`
  const first = await sendNativeAnswer(context, prompt, answer)
  const reply = `The slash command reached the model as text: REPLY${marker}.`
  const start = await modelScript.queue(nativeTextStep(context, reply))
  await sendMessage(page, options.markCommand ? modelScript.prompt('/compact') : '/compact')
  await modelScript.waitForSteps(start + 1)
  await waitForAgentIdle(page)
  // Read the record after the turn, because a native client states more of its request later.
  const request = await modelScript.requestAt(start)
  expect((options.lastUserText ?? nativeModelLastUserText)(request), 'the last user text of the command request').toContain('/compact')
  const modelContext = nativeScenarioModelContextText(context, request)
  expect(modelContext).toContain(prompt)
  expect(modelContext).toContain(answer)
  await expect(assistantBubbles(page).filter({ hasText: reply }).first()).toBeVisible()
  await expect(compactionNoticeRow(page)).toHaveCount(0)
  return { first, request, prompt, answer }
}
