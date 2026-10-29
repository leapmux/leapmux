import { expect } from '@playwright/test'
import { exerciseModelError, exerciseTransientModelFailure } from '../helpers/nativeModelError'
import { assistantBubbles, messageContents, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest('shows the native model failure and accepts a later valid prompt', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running' })
})

opencodeTest('keeps only the retried answer after a mid-stream failure', async ({ native }) => {
  const { page, modelScript } = native
  const start = await modelScript.queue(
    {
      error: { status: 503, message: 'Service unavailable', midStream: true },
    },
    { text: 'FULL-ANSWER-AFTER-RETRY' },
  )
  await sendMessage(page, modelScript.prompt('Stream the scripted answer.'))
  await modelScript.waitForSteps(start + 2)
  await waitForAgentIdle(page)
  for (const reload of [false, true]) {
    if (reload)
      await page.reload()
    await expect(assistantBubbles(page).filter({ hasText: 'FULL-ANSWER-AFTER-RETRY' })).toHaveCount(1)
    await expect(messageContents(page).filter({ hasText: 'partial ' })).toHaveCount(0)
    const retry = messageContents(page).filter({ hasText: 'API retry' })
    await expect(retry).toHaveCount(1)
    await expect(retry).toContainText('succeeded')
  }
})

opencodeTest('schedules LeapMux\'s own server-error retry after the native client gives up', async ({ native }) => {
  // OpenCode 1.19.4 retries a 5xx five times. Without a `retry-after` header its
  // backoff starts at 2 s and doubles, about 70 s in all. It waits as long as the
  // header states instead, so each error states one second.
  await exerciseTransientModelFailure(native, {
    error: {
      status: 500,
      code: 'server_error',
      message: 'The server had an error while processing your request.',
      retryAfterSeconds: 1,
    },
    attempts: 6,
  })
})

opencodeTest('stops a turn whose native retry waits for hours and schedules LeapMux\'s own retry', async ({ native }) => {
  // OpenCode 1.19.4 waits as long as a `retry-after` header states, inside the open turn,
  // and states the instant of its next attempt. LeapMux ends a turn whose next
  // attempt lies more than 10 minutes ahead, and schedules its own retry for
  // that instant, which a restart keeps.
  await exerciseTransientModelFailure(native, {
    error: {
      status: 429,
      code: 'rate_limit_exceeded',
      message: 'Rate limit reached for requests.',
      retryAfterSeconds: 5 * 60 * 60,
    },
    attempts: 1,
    retryKind: 'usage limit',
  })
})
