import type { NativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { nativeScenarioModelContextText } from '../helpers/nativeScenario'
import { exerciseCompactAsModelText } from '../helpers/unsupportedCompaction'

/**
 * Prove that Letta Code's App Server path sends `/compact` to the model as ordinary text, and that the next turn
 * keeps the earlier context. The command carries the scenario marker, so the mock routes its request by the command.
 */
export async function exerciseOrdinaryCompactText(context: NativeScenarioContext): Promise<void> {
  const { prompt, answer } = await exerciseCompactAsModelText(context, { markCommand: true })
  const next = await sendNativeAnswer(context, 'Continue the prior conversation.', 'The later task ended.')
  const nextContext = nativeScenarioModelContextText(context, next)
  expect(nextContext).toContain(prompt)
  expect(nextContext).toContain(answer)
  // One request for each ordered step: the command started no summarizer request of its own.
  const status = await context.modelScript.status()
  expect(status.requests.filter(record => record.stepIndex !== undefined)).toHaveLength(status.nextStep)
}
