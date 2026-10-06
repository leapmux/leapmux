import type { Locator } from '@playwright/test'
import type { MockModelRequestRecord } from './mockModelScript'
import type { NativeScenarioContext } from './nativeScenario'
import type { QuestionRequest } from './providerToolCalls'
import { expect } from '@playwright/test'
import { nativeTextStep, nativeToolOutcome } from './nativeScenario'
import { askUserQuestionToolCall } from './providerToolCalls'
import { assistantBubbles, controlButton, expectNoControlBanner, sendMessage, waitForAgentIdle, waitForControlBanner } from './ui'

/**
 * A question that serves as a real native control in a proof that another control stays absent.
 * The proof picks Green, the second option, so a reply that states the first option or no option fails it.
 */
export const NATIVE_CONTROL_QUESTION: QuestionRequest = {
  question: 'Choose the native control proof.',
  header: 'Proof',
  options: [{ label: 'Blue', description: 'Use blue.' }, { label: 'Green', description: 'Use green.' }],
}

/** One native question turn: the model asks, the reader answers in the banner, and the model answers after it. */
export interface QuestionAnswerOptions {
  /** The questions of the native question call. The banner must show the first one. */
  questions: readonly QuestionRequest[]
  /**
   * Answer the visible banner, which holds the first question. The reply sends the answer, for example through Submit.
   * It carries every provider difference of the answer: a typed answer, a note, several picks, a form.
   */
  reply: (banner: Locator) => Promise<void>
  /** The ID of the scripted question call. */
  callId?: string
  /** The prompt that starts the turn. The turn marks it for this test's script. */
  prompt?: string
  /** The model's answer after it reads the reply. It goes through `nativeTextStep`, so a provider that answers through a tool keeps its own form. */
  answer?: string
  /**
   * Read the reply from the request that follows the question.
   * By default the context's tool result reader reads the result of the question call, or the generic reader does.
   * A provider whose reader states only one tool, or whose reply arrives outside the tool result, passes its own.
   */
  readResult?: (request: MockModelRequestRecord, callId: string) => string | Promise<string>
}

/** The outcome of one native question turn. */
export interface QuestionAnswerTurn {
  /** The reply that the model read, as `readResult` states it. */
  result: string
  /** The model request that followed the question. It carries the reply. */
  request: MockModelRequestRecord
}

/**
 * Run one native question turn and return the reply that the model read after it.
 *
 * The turn queues the question call and the answer, sends the prompt, waits for the visible banner with the first
 * question, and lets `reply` answer it. It then waits for the answer step and the idle agent, reads the reply from the
 * request after the question, and requires the answer and no control banner, visible or hidden.
 * The caller checks the reply itself, because the reply proves which answer the reader chose.
 */
export async function exerciseQuestionAnswer(context: NativeScenarioContext, options: QuestionAnswerOptions): Promise<QuestionAnswerTurn> {
  const first = options.questions[0]
  if (!first)
    throw new Error('A native question turn needs at least one question.')
  const callId = options.callId ?? 'native-question'
  const answer = options.answer ?? 'The native question answer was recorded.'
  const start = await context.modelScript.queue(
    { toolCalls: [askUserQuestionToolCall(context.provider, callId, [...options.questions])] },
    nativeTextStep(context, answer),
  )
  await sendMessage(context.page, context.modelScript.prompt(options.prompt ?? 'Ask the scripted question, then report the answer.'))
  await context.modelScript.waitForSteps(start + 1)
  const banner = await waitForControlBanner(context.page)
  await expect(banner).toContainText(first.question)
  await options.reply(banner)
  await context.modelScript.waitForSteps(start + 2)
  await waitForAgentIdle(context.page)
  const request = await context.modelScript.requestAt(start + 1)
  const result = options.readResult
    ? await options.readResult(request, callId)
    : (await nativeToolOutcome(context, request, callId)).text
  await expect(assistantBubbles(context.page).filter({ hasText: answer }).first()).toBeVisible()
  await expectNoControlBanner(context.page)
  return { result, request }
}

/**
 * Return a reply that picks the option `label` of the banner, then sends the answer through the visible Submit.
 * The pick looks only inside the banner, so an option of another surface cannot take the click.
 */
export function chooseQuestionOption(label: string): (banner: Locator) => Promise<void> {
  if (!label)
    throw new Error('A question option needs a label.')
  return async (banner) => {
    await banner.getByTestId(`question-option-${label}`).click()
    await controlButton(banner.page(), 'submit').click()
  }
}
