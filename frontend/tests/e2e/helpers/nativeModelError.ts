import type { MockModelError } from './mockModelScript'
import type { NativeScenarioContext } from './nativeScenario'
import { randomUUID } from 'node:crypto'
import { expect } from '@playwright/test'
import { sendNativeAnswer } from './nativeConversation'
import { sendMessage, visibleOnly, waitForAgentIdle } from './ui'

interface NativeModelErrorOptions {
  error?: MockModelError
  prepare?: () => Promise<void>
}

/** Verify a native service failure and a successful later turn through the same tab. */
export async function exerciseModelError(
  context: NativeScenarioContext,
  options: NativeModelErrorOptions = {},
): Promise<void> {
  await options.prepare?.()
  const marker = `NATIVEERROR${randomUUID().replaceAll('-', '')}`
  const error = options.error ?? { status: 400, code: 'invalid_request_error', message: marker }
  const stepIndex = (await context.modelScript.status()).stepCount
  await context.modelScript.queue({ error })
  await sendMessage(context.page, context.modelScript.prompt('Run the native model error probe.'))
  const status = await context.modelScript.waitForSteps(stepIndex + 1)
  expect(status.requests.some(request => request.stepIndex === stepIndex)).toBe(true)
  await waitForAgentIdle(context.page)
  await expect(visibleOnly(context.page.getByText(error.message, { exact: false })).first()).toBeVisible()
  await expect(context.page.locator('[data-testid="thinking-indicator"]:visible')).toHaveCount(0)
  const queue = context.page.locator('[data-testid="queue-pause-button"]:visible')
  if (await queue.count() > 0 && (await queue.textContent())?.includes('Resume'))
    await queue.click()
  await sendNativeAnswer(context, 'Reply once after the native service failure.', `RECOVERED${marker}`)
}
