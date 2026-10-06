import type { Locator } from '@playwright/test'
import type { NativeScenarioContext } from '../helpers/nativeScenario'
import { randomUUID } from 'node:crypto'
import { expect } from '@playwright/test'
import { exerciseQuestionAnswer } from '../helpers/nativeQuestion'

/**
 * Ask Dirac's native color question, let `reply` answer its form, and return the native result that the model read.
 *
 * Dirac draws the question as a form in the banner, so the reply drives the form and its own buttons. The answer
 * after the reply goes through Dirac's respond tool, which the context's text step builds.
 */
export async function exerciseQuestionReply(
  context: NativeScenarioContext,
  reply: (form: Locator) => Promise<void>,
): Promise<string> {
  const { result } = await exerciseQuestionAnswer(context, {
    questions: [{
      question: 'Which color should I use?',
      header: 'Color',
      options: [{ label: 'Blue', description: 'Use blue.' }, { label: 'Red', description: 'Use red.' }],
    }],
    callId: `dirac-question-${randomUUID()}`,
    prompt: 'Ask the scripted color question and then complete.',
    answer: 'The native question turn completed.',
    reply: async (banner) => {
      const form = banner.getByTestId('elicitation-form')
      await expect(form).toBeVisible()
      await reply(form)
    },
  })
  return result
}
