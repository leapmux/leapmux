import type { MockModelError } from './mockModelScript'
import type { NativeScenarioContext } from './nativeScenario'
import { expect } from '@playwright/test'
import { nativeTextStep } from './nativeScenario'
import { uniqueMarker } from './shellArguments'
import { assistantBubbles, messageContents, sendMessage, waitForAgentIdle } from './ui'

export interface NativeModelRetryOptions {
  errors: readonly MockModelError[]
  answer?: string
}

/** Require one clean answer and one resolved retry row, live and after a reload. */
export async function exerciseModelRetry(context: NativeScenarioContext, options: NativeModelRetryOptions): Promise<void> {
  if (options.errors.length === 0)
    throw new Error('The retry scenario needs at least one failed attempt.')
  const answer = options.answer ?? `RETRIEDANSWER${uniqueMarker()}`
  if (!answer.trim())
    throw new Error('The retry scenario needs a nonempty answer.')
  const { page, modelScript } = context
  const start = await modelScript.queue(...options.errors.map(error => ({ error })), nativeTextStep(context, answer))
  await sendMessage(page, modelScript.prompt('Run the scripted transient model failure.'))
  await modelScript.waitForSteps(start + options.errors.length + 1)
  await waitForAgentIdle(page)
  for (const reload of [false, true]) {
    if (reload) {
      await page.reload()
      await waitForAgentIdle(page)
    }
    await expect(assistantBubbles(page).filter({ hasText: answer })).toHaveCount(1)
    await expect(messageContents(page).filter({ hasText: answer })).toHaveText(answer)
    await expect(messageContents(page).filter({ hasText: /API retry.*succeeded/ })).toHaveCount(1)
    await expect(page.locator('[data-testid="result-divider"]:visible').filter({ hasText: /failed/i })).toHaveCount(0)
    if (options.errors.some(error => error.midStream))
      await expect(assistantBubbles(page).filter({ hasText: 'partial ' })).toHaveCount(0)
  }
}
