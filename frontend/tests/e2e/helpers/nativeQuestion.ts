import type { Locator } from '@playwright/test'
import type { MockModelRequestRecord, MockModelStep } from './mockModelScript'
import type { ManagedNativeScenarioContext, NativeScenarioContext } from './nativeScenario'
import type { QuestionRequest } from './providerToolCalls'
import { expect } from '@playwright/test'
import { accountStorageKey, PREFIX_CONTROL_STATE } from '../../../src/lib/browserStorage'
import { isObject } from '../../../src/lib/jsonPick'
import { getUserId } from './api'
import { expectTurnEndedAfter } from './modelScriptFixture'
import { nativeTextStep, nativeToolOutcome } from './nativeScenario'
import { askUserQuestionToolCall } from './providerToolCalls'
import { waitForStoredEntry } from './storage'
import { assistantBubbles, composerEditor, controlAgentId, controlBanner, controlButton, expectNoControlBanner, questionPagination, readAttached, sendMessage, waitForAgentIdle, waitForControlBanner, waitForEditorDraft } from './ui'

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

type QuestionStartOptions = Pick<QuestionAnswerOptions, 'questions' | 'reply' | 'callId' | 'prompt'>

/** Raise the native question and return its exact script position and visible banner. */
async function startQuestion(context: NativeScenarioContext, options: QuestionStartOptions, continuation: readonly MockModelStep[]) {
  const first = options.questions[0]
  if (!first)
    throw new Error('A native question turn needs at least one question.')
  const callId = options.callId ?? 'native-question'
  const start = await context.modelScript.queue(
    { toolCalls: [askUserQuestionToolCall(context.provider, callId, [...options.questions])] },
    ...continuation,
  )
  await sendMessage(context.page, context.modelScript.prompt(options.prompt ?? 'Ask the scripted question, then report the answer.'))
  await context.modelScript.waitForSteps(start + 1)
  const banner = await waitForControlBanner(context.page)
  await expect(banner).toContainText(first.question)
  return { start, callId, banner }
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
  const answer = options.answer ?? 'The native question answer was recorded.'
  const { start, callId, banner } = await startQuestion(context, options, [nativeTextStep(context, answer)])
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

/** Cancel a native question whose cancellation ends the turn without another model request. */
export async function exerciseQuestionCancellation(context: NativeScenarioContext, options: QuestionStartOptions): Promise<void> {
  const { start, banner } = await startQuestion(context, options, [])
  await options.reply(banner)
  await expectNoControlBanner(context.page)
  await waitForAgentIdle(context.page)
  await expectTurnEndedAfter(context.modelScript, start + 1)
}

/**
 * Pick the option `label` of the question that `banner` shows, and send nothing. The pick looks only inside the
 * banner, so an option of another surface cannot take the click. A banner of several questions takes one pick for
 * each question before its one Submit, so its flow picks through this function and submits by itself.
 */
export async function pickQuestionOption(banner: Locator, label: string): Promise<void> {
  if (!label)
    throw new Error('A question option needs a label.')
  const option = banner.getByTestId(`question-option-${label}`)
  await expect(option).toBeVisible()
  await option.click()
}

/** Return a reply that picks the option `label` of the banner, then sends the answer through the visible Submit. */
export function chooseQuestionOption(label: string, context?: ManagedNativeScenarioContext): (banner: Locator) => Promise<void> {
  if (!label)
    throw new Error('A question option needs a label.')
  return async (banner) => {
    await pickQuestionOption(banner, label)
    if (context)
      await restoreQuestionSelection(context, banner, label)
    await controlButton(banner.page(), 'submit').click()
  }
}

/** Submit an option and its independent note after both survive a reload. */
export function chooseQuestionOptionWithNote(context: ManagedNativeScenarioContext, label: string, note: string): (banner: Locator) => Promise<void> {
  if (!label || !note.trim())
    throw new Error('A selected question answer with a note requires a label and nonempty note.')
  return async (banner) => {
    await pickQuestionOption(banner, label)
    await restoreQuestionSelection(context, banner, label)
    await composerEditor(context.page).fill(note)
    await restoreQuestionTextDraft(context, note)
    const restored = await waitForControlBanner(context.page)
    await expect(restored.getByTestId(`question-option-${label}`).locator('input')).toBeChecked()
    await controlButton(context.page, 'submit').click()
  }
}

/** Require the selected native value in durable storage and on the restored question before submitting it. */
export async function restoreQuestionSelection(context: ManagedNativeScenarioContext, banner: Locator, label: string, pageIndex?: number): Promise<void> {
  validateQuestionPageIndex(pageIndex)
  const selected = banner.getByTestId(`question-option-${label}`).locator('input')
  await expect(selected).toBeChecked()
  const value = await selected.inputValue()
  if (!value)
    throw new Error('The selected question option has no native value.')
  const agentId = await controlAgentId(banner)
  const server = context.leapmuxServer
  const userId = server.adminUserId ?? await getUserId(server.hubUrl, server.adminToken)
  await waitForStoredEntry(
    context.page,
    accountStorageKey(userId, `${PREFIX_CONTROL_STATE}${agentId}:`),
    stored => isObject(stored) && isObject(stored.selections)
      && (pageIndex === undefined
        ? Object.values(stored.selections).some(values => Array.isArray(values) && values.includes(value))
        : stored.currentPage === pageIndex && Array.isArray(stored.selections[pageIndex]) && stored.selections[pageIndex].includes(value)),
    'the selected answer reaches durable storage before reload',
  )
  await context.page.reload()
  const restored = await waitForControlBanner(context.page)
  expect(await controlAgentId(restored), 'the restored question belongs to the original agent').toBe(agentId)
  await expect(restored.getByTestId(`question-option-${label}`).locator('input')).toBeChecked()
}

/** Restore a typed question answer only after its draft reaches durable account storage. */
export async function restoreQuestionTextDraft(context: ManagedNativeScenarioContext, text: string, pageIndex?: number): Promise<void> {
  validateQuestionPageIndex(pageIndex)
  if (!text.trim())
    throw new Error('A question draft proof requires nonempty text.')
  const agentId = await controlAgentId(controlBanner(context.page))
  const server = context.leapmuxServer
  const userId = server.adminUserId ?? await getUserId(server.hubUrl, server.adminToken)
  await expect(composerEditor(context.page)).toHaveText(text)
  await waitForEditorDraft(context.page, userId, text)
  if (pageIndex !== undefined) {
    await waitForStoredEntry(
      context.page,
      accountStorageKey(userId, `${PREFIX_CONTROL_STATE}${agentId}:`),
      stored => isObject(stored) && stored.currentPage === pageIndex,
      'the question page reaches durable storage before reload',
    )
  }
  await context.page.reload()
  const restored = await waitForControlBanner(context.page)
  expect(await controlAgentId(restored), 'the restored draft belongs to the original agent').toBe(agentId)
  await expect(composerEditor(context.page)).toHaveText(text)
}

/** The ID of the native question call that {@link askQuestions} scripts. The request after it holds the answer. */
export const ASKED_QUESTIONS_CALL_ID = 'ask-user'

/** The answer of each model request that follows the question call of {@link askQuestions}. */
export const ASKED_QUESTIONS_ANSWER = 'You answered the questions.'

/**
 * Script one native question call, send the turn that makes it, and return the number of model requests before the
 * turn. The caller finds the request that carries the answer after that number.
 *
 * The helper waits for the model request that supplies the question call. With `gate`, the question call holds at the gate instead,
 * and the turn returns when the call waits there, so the caller decides when the banner appears.
 *
 * A fallback answers each request after the question call with {@link ASKED_QUESTIONS_ANSWER}. What the reader does
 * with the banner decides how many requests follow: an answer, a refusal and a stop each lead to a different count.
 */
export async function askQuestions(
  context: NativeScenarioContext,
  questions: readonly QuestionRequest[],
  options: { gate?: string } = {},
): Promise<number> {
  if (questions.length === 0)
    throw new Error('A native question call needs at least one question.')
  const before = (await context.modelScript.status()).requests.length
  await context.modelScript.fallback(nativeTextStep(context, ASKED_QUESTIONS_ANSWER))
  await context.modelScript.queue({
    toolCalls: [askUserQuestionToolCall(context.provider, ASKED_QUESTIONS_CALL_ID, [...questions])],
    ...(options.gate === undefined ? {} : { gate: options.gate }),
  })
  await sendMessage(context.page, context.modelScript.prompt('Ask the scripted questions and tell me what I answered.'))
  if (options.gate === undefined)
    await context.modelScript.waitForSteps()
  else
    await context.modelScript.waitForGate(options.gate)
  return before
}

export type QuestionPageAnswer = { labels: readonly string[] } | { text: string }

/** Answer every native question page and verify all answers after durable reloads. */
export function chooseQuestionPages(
  context: ManagedNativeScenarioContext,
  questions: readonly QuestionRequest[],
  answers: readonly QuestionPageAnswer[],
): (banner: Locator) => Promise<void> {
  if (questions.length === 0 || questions.length !== answers.length)
    throw new Error('Question pages require one answer for each question.')
  for (const [index, answer] of answers.entries()) {
    if ('text' in answer) {
      if (!answer.text.trim())
        throw new Error('A typed question-page answer must contain text.')
      continue
    }
    const question = questions[index]!
    if (answer.labels.length === 0 || new Set(answer.labels).size !== answer.labels.length
      || answer.labels.some(label => !question.options.some(option => option.label === label))) {
      throw new Error('A selected question-page answer requires distinct offered options.')
    }
    if (!question.multiSelect && answer.labels.length !== 1)
      throw new Error('A single-choice question requires one selected option.')
  }
  return async (banner) => {
    const page = context.page
    const openPage = async (index: number) => {
      if (questions.length > 1) {
        const buttons = questionPagination(page).locator('button')
        await expect(buttons).toHaveCount(questions.length)
        await buttons.nth(index).click()
      }
      await expect(banner).toContainText(questions[index]!.question)
    }
    for (const [index, answer] of answers.entries()) {
      await openPage(index)
      if ('text' in answer) {
        await composerEditor(page).fill(answer.text)
        await restoreQuestionTextDraft(context, answer.text, questions.length > 1 ? index : undefined)
      }
      else {
        for (const label of answer.labels)
          await pickQuestionOption(banner, label)
        // A single choice can advance the page. Return before verifying its stored value.
        await openPage(index)
        await restoreQuestionSelection(context, banner, answer.labels.at(-1)!, questions.length > 1 ? index : undefined)
      }
      await expect(banner).toContainText(questions[index]!.question)
    }
    // The final reload must retain earlier pages also, including every selected option.
    for (const [index, answer] of answers.entries()) {
      await openPage(index)
      if ('text' in answer) {
        await expect(composerEditor(page)).toHaveText(answer.text)
      }
      else {
        for (const label of answer.labels)
          await expect(banner.getByTestId(`question-option-${label}`).locator('input')).toBeChecked()
      }
    }
    await controlButton(page, 'submit').click()
  }
}

/** Verify two native answer pages, including multiple selection when the native tool offers it. */
export async function exerciseQuestionPages(
  context: ManagedNativeScenarioContext,
  options: Pick<QuestionAnswerOptions, 'readResult'> & { multiple?: boolean } = {},
): Promise<QuestionAnswerTurn> {
  const multiple = options.multiple ?? true
  const questions: QuestionRequest[] = [
    {
      header: 'Color',
      question: 'Which native color should this test use?',
      options: [{ label: 'Blue', description: 'Use blue.' }, { label: 'Green', description: 'Use green.' }],
    },
    {
      header: 'Size',
      question: 'Which native sizes should this test use?',
      multiSelect: multiple,
      options: [{ label: 'Small', description: 'Use small.' }, { label: 'Medium', description: 'Use medium.' }, { label: 'Large', description: 'Use large.' }],
    },
  ]
  const result = await exerciseQuestionAnswer(context, {
    questions,
    callId: 'native-question-pages',
    reply: chooseQuestionPages(context, questions, [{ labels: ['Green'] }, { labels: multiple ? ['Large', 'Small'] : ['Large'] }]),
    ...(options.readResult ? { readResult: options.readResult } : {}),
  })
  expect(result.result).toContain('Green')
  expect(result.result).toContain('Large')
  expect(result.result).not.toContain('Blue')
  expect(result.result).not.toContain('Medium')
  if (multiple)
    expect(result.result).toContain('Small')
  else
    expect(result.result).not.toContain('Small')
  return result
}

/** Refuse an invalid question page before reading browser state. */
function validateQuestionPageIndex(pageIndex: number | undefined): void {
  if (pageIndex !== undefined && (!Number.isSafeInteger(pageIndex) || pageIndex < 0))
    throw new Error('A question page index requires a nonnegative safe integer.')
}

export const NATIVE_QUESTION_CODE_PREVIEW = 'const color = "green"'

/** Verify native previews in a narrow layout before and after restoring the selected answer. */
export async function exerciseQuestionPreviews(context: ManagedNativeScenarioContext): Promise<QuestionAnswerTurn> {
  await context.page.setViewportSize({ width: 600, height: 900 })
  const diagram = '┌────────┐\n│ sample │\n└────────┘'
  const expectPreviews = async (banner: Locator) => {
    await expect(banner.getByText('Choose the color blue.', { exact: true })).toBeVisible()
    await expect(banner.getByText('Choose the color green.', { exact: true })).toBeVisible()
    const preview = banner.getByRole('region', { name: 'Blue preview' })
    await expect(preview).toContainText('│ sample │')
    await expect(preview).toHaveCSS('white-space', 'pre')
    await expect(banner.getByRole('region', { name: 'Green preview' }).locator('pre code')).toContainText(NATIVE_QUESTION_CODE_PREVIEW)
    await expect(banner.getByRole('region', { name: 'Green preview' })).toBeInViewport()
    const size = await readAttached(banner, 'the native question preview banner', (matches) => {
      const element = matches.find(element => element.isConnected)
      return element ? { width: element.clientWidth, scroll: element.scrollWidth } : null
    })
    expect(size.width).toBeGreaterThan(0)
    expect(size.scroll).toBeLessThanOrEqual(size.width)
  }
  const turn = await exerciseQuestionAnswer(context, {
    questions: [{
      question: 'Pick a color.',
      header: 'Color',
      options: [
        { label: 'Blue', description: 'Choose the color blue.', preview: diagram },
        { label: 'Green', description: 'Choose the color green.', preview: `\`\`\`ts\n${NATIVE_QUESTION_CODE_PREVIEW}\n\`\`\`` },
      ],
    }],
    callId: 'color-question',
    reply: async (banner) => {
      await expectPreviews(banner)
      await pickQuestionOption(banner, 'Green')
      await restoreQuestionSelection(context, banner, 'Green')
      await expectPreviews(banner)
      await expect(controlButton(context.page, 'submit')).toBeEnabled()
      await controlButton(context.page, 'submit').click()
    },
  })
  expect(turn.result).toContain('Green')
  expect(turn.result).not.toContain('Blue')
  return turn
}

/** Send a restored custom answer and require its exact text in the native tool result. */
export async function exerciseTypedQuestionAnswer(
  context: ManagedNativeScenarioContext,
  options: Pick<QuestionAnswerOptions, 'readResult'> = {},
): Promise<QuestionAnswerTurn> {
  const text = 'A custom native answer with zero 0 and Unicode 界.'
  const questions = [NATIVE_CONTROL_QUESTION]
  const turn = await exerciseQuestionAnswer(context, {
    questions,
    callId: 'typed-native-question',
    reply: chooseQuestionPages(context, questions, [{ text }]),
    ...options,
  })
  expect(turn.result).toContain(text)
  for (const option of NATIVE_CONTROL_QUESTION.options)
    expect(turn.result).not.toContain(option.label)
  return turn
}
