import type { MockModelRequestRecord } from './mockModelScript'
import type { NativeScenarioContext } from './nativeScenario'
import { randomUUID } from 'node:crypto'
import { expect } from '@playwright/test'
import { nativeScenarioModelContextText, nativeTextStep } from './nativeScenario'
import { assistantBubbles, sendMessage, userBubbles, waitForAgentIdle } from './ui'

/** Run a real native turn and return the model request that consumed its prompt. */
export async function sendNativeAnswer(
  context: NativeScenarioContext,
  prompt: string,
  answer: string,
): Promise<MockModelRequestRecord> {
  const stepIndex = (await context.modelScript.status()).stepCount
  await context.modelScript.queue(nativeTextStep(context, answer))
  await sendMessage(context.page, context.modelScript.prompt(prompt))
  const status = await context.modelScript.waitForSteps(stepIndex + 1)
  await waitForAgentIdle(context.page)
  const request = status.requests.find(record => record.stepIndex === stepIndex)
  if (!request)
    throw new Error('The scripted answer reached no native model request.')
  expect(nativeScenarioModelContextText(context, request)).toContain(prompt)
  await expect(assistantBubbles(context.page).filter({ hasText: answer }).first()).toBeVisible()
  return request
}

/** Verify native prompt delivery, completed output, and saved browser transcript rows. */
export async function exerciseBasicChat(context: NativeScenarioContext): Promise<void> {
  const marker = randomUUID().replaceAll('-', '')
  const prompt = `Reply once for BASICCHAT${marker}.`
  const answer = `BASICANSWER${marker}`
  await sendNativeAnswer(context, prompt, answer)
  await expect(userBubbles(context.page).filter({ hasText: prompt }).first()).toBeVisible()
  await expect(context.page.locator('[data-testid="thinking-indicator"]:visible')).toHaveCount(0)
  await expect(context.page.locator('[data-testid="result-divider"]:visible').last()).toBeVisible()
  await context.page.reload()
  await expect(userBubbles(context.page).filter({ hasText: prompt }).first()).toBeVisible()
  await expect(assistantBubbles(context.page).filter({ hasText: answer }).first()).toBeVisible()
}

/** Prove that a later native request can use the first prompt and answer. */
export async function exerciseConversationContext(context: NativeScenarioContext): Promise<void> {
  const marker = randomUUID().replaceAll('-', '')
  const firstPrompt = `Keep CONTEXTPROMPT${marker} for the conversation.`
  const firstAnswer = `CONTEXTANSWER${marker}`
  await sendNativeAnswer(context, firstPrompt, firstAnswer)
  const next = await sendNativeAnswer(context, 'Continue the existing conversation once.', `NEXTANSWER${marker}`)
  const nativeContext = nativeScenarioModelContextText(context, next)
  expect(nativeContext).toContain(firstPrompt)
  expect(nativeContext).toContain(firstAnswer)
  await expect(userBubbles(context.page)).toHaveCount(2)
  await expect(context.page.locator('[data-testid="result-divider"]:visible')).toHaveCount(2)
}
