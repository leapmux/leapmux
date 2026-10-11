import type { Page } from '@playwright/test'
import type { ModelScript } from './modelScriptFixture'
import type { MessageEntry } from './ui'
import { escapeRegExp } from '../../../src/lib/regexp'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, expectAssistantAnswer, sendMessage, waitForAgentIdle } from './ui'

/** One scripted chat turn: the prompt that the test sends, and the answer that the model script returns for it. */
export interface ScriptedTurn {
  /** The prompt. The helper marks it for the script of the test, so the caller passes the plain text. */
  prompt: string
  /**
   * The answer. It must render as its own text, because the helper finds it in an assistant bubble: plain prose,
   * with no Markdown syntax that the transcript replaces.
   */
  answer: string
  /** How the prompt enters the composer. The default types it. */
  entry?: MessageEntry
}

/** The arithmetic turn that most chat tests use. */
export const ARITHMETIC_TURN: ScriptedTurn = { prompt: ARITHMETIC_PROMPT, answer: ARITHMETIC_ANSWER_TEXT }

/** A sentence of plain words, for a test that selects or quotes the text of an answer. */
export const QUICK_BROWN_FOX = 'The quick brown fox jumps over the lazy dog'

/** A turn that asks the model to repeat `text`, and whose scripted answer is `text`. */
export function sayExactly(text: string): ScriptedTurn {
  return { prompt: `Say exactly: ${text}`, answer: text }
}

/**
 * Send one scripted chat turn and wait until it ends. Return the step index of the answer in the script.
 *
 * - The answer goes to the end of the ordered queue of the script. The wait is for THAT step, so an earlier turn, or
 *   a later turn that the test queued already, cannot end the wait early or make it wait for the wrong turn.
 * - The answer must reach an assistant bubble as a whole word sequence, so `ok` does not match `tokens` in a status
 *   row. The thinking indicator must then go away, which is the end of the turn in the UI.
 */
export async function sendScriptedTurn(page: Page, script: ModelScript, turn: ScriptedTurn = ARITHMETIC_TURN): Promise<number> {
  if (turn.answer.trim() === '')
    throw new Error('A scripted turn needs an answer that the transcript can show.')
  const step = await script.queue({ text: turn.answer })
  await sendMessage(page, script.prompt(turn.prompt), turn.entry)
  await script.waitForSteps(step + 1)
  await expectAssistantAnswer(page, { answer: wholeText(turn.answer) })
  await waitForAgentIdle(page)
  return step
}

/** Match `text` where no word character touches either end, so a short answer does not match inside a longer word. */
export function wholeText(text: string): RegExp {
  return new RegExp(`(?<!\\w)${escapeRegExp(text)}(?!\\w)`)
}
