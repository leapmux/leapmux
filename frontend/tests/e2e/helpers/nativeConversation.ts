import type { Page } from '@playwright/test'
import type { MockModelRequestRecord, MockModelStep, MockModelToolCall } from './mockModelScript'
import type { NativeScenarioContext } from './nativeScenario'
import type { MessageEntry } from './ui'
import { expect } from '@playwright/test'
import { pluralize } from '../../../src/lib/plural'
import { nativeModelConversationTurns, nativeScenarioModelContextText, nativeTextStep } from './nativeScenario'
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
export async function expectNoUnrenderedRow(page: Page): Promise<void> {
  for (const title of UNRENDERED_ROW_TITLES)
    await expect(visibleOnly(page.getByText(title, { exact: true })), `the chat draws no "${title}" card`).toHaveCount(0)
}

/**
 * Require that the last user turn of a native model request holds `prompt`.
 *
 * A check of the whole request also passes when the prompt reaches the model only in an instruction or in an earlier
 * row, while the native agent sends a different user row last. A provider whose request does not state its history in
 * a generic model API shape, or whose native rows the generic reader would misread as conversation, supplies its own
 * turn reader (`readConversationTurns`), which classifies the engine's own rows as context.
 */
function expectPromptInLastUserTurn(
  context: Pick<NativeScenarioContext, 'readConversationTurns'>,
  request: MockModelRequestRecord,
  prompt: string,
): void {
  const userTurns = (context.readConversationTurns ?? nativeModelConversationTurns)(request).filter(turn => turn.role === 'user')
  const promptTurn = userTurns.at(-1)
  expect(promptTurn, 'the native request holds a user turn that carries the prompt').toBeDefined()
  expect(promptTurn?.text, 'the last user turn of the native request holds the prompt').toContain(prompt)
}

/** A duration as `formatDuration` (`src/components/chat/rendererUtils.ts`) states it: `377ms`, `2.3s` or `1m 5s`. */
const DURATION_PATTERN = String.raw`(?:\d+ms|\d+\.\d+s|\d+[dhms](?: \d+[dhms])*)`

/** A cost as `dividerTotals` (`src/components/chat/resultDividerRenderers.tsx`) states it. */
const COST_PATTERN = String.raw`\$\d+\.\d{4}`

/**
 * The totals that the divider draws after its label, as `dividerTotals` states them: the tool count of a turn that
 * called a tool, then the cost of a turn that the provider priced. The cost depends on the native price list, so the
 * pattern accepts its absence.
 */
function dividerTotalsPattern(toolCount: number): string {
  if (toolCount === 0)
    return `(?:${COST_PATTERN})?`
  return `${pluralize(toolCount, 'tool')}(?: \u00B7 ${COST_PATTERN})?`
}

/**
 * The number of the tool calls of `step` that the transcript draws as tool rows. A provider can deliver its answer
 * through a native tool call. The transcript draws that call as a tool row, unless the provider lists its name in
 * `answerToolNames`.
 */
function drawnToolCallCount(context: Pick<NativeScenarioContext, 'answerToolNames'>, step: MockModelStep): number {
  return (step.toolCalls ?? []).filter(call => !(context.answerToolNames ?? []).includes(call.name)).length
}

/**
 * The text of the divider that ends a basic chat turn. The label is the shared outcome words (`turnEndLabel` in the
 * chat), with the duration exactly when the provider states one. The totals follow the label.
 */
function basicChatDividerText(timedDivider: boolean, toolCount: number): RegExp {
  const duration = timedDivider ? String.raw` \(${DURATION_PATTERN}\)` : ''
  return new RegExp(`^Turn ended${duration}${dividerTotalsPattern(toolCount)}$`)
}

/** What a provider states at the end of a basic chat turn. */
export interface BasicChatOptions {
  /**
   * Whether the turn-end divider states the duration of the turn, as "Turn ended (2.3s)". A provider states it when
   * its native turn end reports the duration, or when its Worker measures the turn. Otherwise the divider states only
   * "Turn ended". The default is false.
   */
  timedDivider?: boolean
}

/** The prompt of one basic chat turn and the model request that consumed it. */
export interface BasicChatTurn {
  /** The prompt without its scenario marker. */
  prompt: string
  request: MockModelRequestRecord
}

/**
 * Verify native prompt delivery, completed output, and saved browser transcript rows.
 *
 * The last user turn of the native request holds the prompt. The turn ends with a "Turn ended" divider that states
 * the duration of the turn exactly when `timedDivider` is set, and the tool count of the tool rows that the answer
 * draws. No row of the turn is an unrendered provider frame. Each check of the chat holds live and after reload.
 * Return the prompt and its request, so a caller can check a provider fact of the request.
 */
export async function exerciseBasicChat(
  context: NativeScenarioContext,
  { timedDivider = false }: BasicChatOptions = {},
): Promise<BasicChatTurn> {
  const marker = uniqueMarker()
  const prompt = `Reply once for BASICCHAT${marker}.`
  const answer = `BASICANSWER${marker}`
  const request = await sendNativeAnswer(context, prompt, answer)
  expectPromptInLastUserTurn(context, request, prompt)
  const dividerText = basicChatDividerText(timedDivider, drawnToolCallCount(context, nativeTextStep(context, answer)))
  const divider = context.page.locator('[data-testid="result-divider"]:visible').last()
  await expect(userBubbles(context.page).filter({ hasText: prompt }).first()).toBeVisible()
  await expect(context.page.locator('[data-testid="thinking-indicator"]:visible')).toHaveCount(0)
  await expect(divider).toHaveText(dividerText)
  await expectNoUnrenderedRow(context.page)
  await context.page.reload()
  await expect(userBubbles(context.page).filter({ hasText: prompt }).first()).toBeVisible()
  await expect(assistantBubbles(context.page).filter({ hasText: answer }).first()).toBeVisible()
  // The saved turn end draws the same divider. A duration that the Worker measured comes from the saved Worker
  // metadata of the row.
  await expect(divider).toHaveText(dividerText)
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
