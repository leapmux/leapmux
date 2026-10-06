import type { Page } from '@playwright/test'
import type { MockModelRequestRecord, MockModelToolCall } from './mockModelScript'
import type { NativeScenarioContext } from './nativeScenario'
import type { MessageEntry } from './ui'
import { expect } from '@playwright/test'
import { nativeScenarioModelContextText, nativeTextStep } from './nativeScenario'
import { uniqueMarker } from './shellArguments'
import { assistantBubbles, sendMessage, userBubbles, visibleOnly, waitForAgentIdle } from './ui'

/** How `sendNativeAnswer` enters the prompt and what the model does before it answers. */
export interface NativeAnswerOptions {
  /** How the prompt reaches the composer. `'insert'` suits a prompt too long to type key by key. */
  entry?: MessageEntry
  /** Tool calls that run before the answer, in the same model step. */
  toolCalls?: readonly MockModelToolCall[]
}

/** Run a real native turn and return the model request that consumed its prompt. */
export async function sendNativeAnswer(
  context: NativeScenarioContext,
  prompt: string,
  answer: string,
  { entry = 'type', toolCalls = [] }: NativeAnswerOptions = {},
): Promise<MockModelRequestRecord> {
  const step = nativeTextStep(context, answer)
  const stepIndex = await context.modelScript.queue(toolCalls.length === 0 ? step : { ...step, toolCalls: [...toolCalls, ...(step.toolCalls ?? [])] })
  await sendMessage(context.page, context.modelScript.prompt(prompt), entry)
  await context.modelScript.waitForSteps(stepIndex + 1)
  await waitForAgentIdle(context.page)
  // Read the record after the turn. The mock counts a step when its request arrives, and a
  // native client states more of that request later, such as the rules of a context query.
  const request = await context.modelScript.requestAt(stepIndex)
  expect(nativeScenarioModelContextText(context, request)).toContain(prompt)
  await expect(assistantBubbles(context.page).filter({ hasText: answer }).first()).toBeVisible()
  return request
}

/**
 * The titles of the card that the chat draws for a provider row that it cannot show: a row that no reader claims, and
 * a row whose reading failed.
 */
const UNRENDERED_ROW_TITLES = ['LeapMux has no display for this row', 'LeapMux could not render this row'] as const

/** Require that the visible chat draws no row as an unrendered provider frame. */
async function expectNoUnrenderedRow(page: Page): Promise<void> {
  for (const title of UNRENDERED_ROW_TITLES)
    await expect(visibleOnly(page.getByText(title, { exact: true })), `the chat draws no "${title}" card`).toHaveCount(0)
}

/** The prompt of one basic chat turn and the model request that consumed it. */
export interface BasicChatTurn {
  /** The prompt without its scenario marker. */
  prompt: string
  request: MockModelRequestRecord
}

/**
 * Verify native prompt delivery, completed output, and saved browser transcript rows.
 * The turn ends with a "Turn ended" divider, and no row of the turn is an unrendered provider frame, live and after
 * reload. Return the prompt and its request, so a caller can check a provider fact of the request.
 */
export async function exerciseBasicChat(context: NativeScenarioContext): Promise<BasicChatTurn> {
  const marker = uniqueMarker()
  const prompt = `Reply once for BASICCHAT${marker}.`
  const answer = `BASICANSWER${marker}`
  const request = await sendNativeAnswer(context, prompt, answer)
  await expect(userBubbles(context.page).filter({ hasText: prompt }).first()).toBeVisible()
  await expect(context.page.locator('[data-testid="thinking-indicator"]:visible')).toHaveCount(0)
  // Every provider states a completed turn with the shared outcome words (`turnEndLabel` in the chat).
  await expect(context.page.locator('[data-testid="result-divider"]:visible').last()).toHaveText(/^Turn ended/)
  await expectNoUnrenderedRow(context.page)
  await context.page.reload()
  await expect(userBubbles(context.page).filter({ hasText: prompt }).first()).toBeVisible()
  await expect(assistantBubbles(context.page).filter({ hasText: answer }).first()).toBeVisible()
  await expectNoUnrenderedRow(context.page)
  return { prompt, request }
}

/**
 * Prove that a later native request can use the first prompt and answer.
 * The first answer stays in the chat after the second turn, and each turn has its own user row and divider.
 */
export async function exerciseConversationContext(context: NativeScenarioContext): Promise<void> {
  const marker = uniqueMarker()
  const firstPrompt = `Keep CONTEXTPROMPT${marker} for the conversation.`
  const firstAnswer = `CONTEXTANSWER${marker}`
  await sendNativeAnswer(context, firstPrompt, firstAnswer)
  const next = await sendNativeAnswer(context, 'Continue the existing conversation once.', `NEXTANSWER${marker}`)
  const nativeContext = nativeScenarioModelContextText(context, next)
  expect(nativeContext).toContain(firstPrompt)
  expect(nativeContext).toContain(firstAnswer)
  await expect(assistantBubbles(context.page).filter({ hasText: firstAnswer }).first()).toBeVisible()
  await expect(userBubbles(context.page)).toHaveCount(2)
  await expect(context.page.locator('[data-testid="result-divider"]:visible')).toHaveCount(2)
}
