import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { grokTest } from '../grok-fixtures'
import { openNativeAgent } from '../helpers/nativeAgentOpen'
import { withNativeConfigurationFile } from '../helpers/nativeConfigurationFile'
import { exerciseModelError, exerciseTransientModelFailure } from '../helpers/nativeModelError'
import { getGlobalState } from '../helpers/server'
import { assistantBubbles, messageContents, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { nativeContext } from './scenarios'

/**
 * Grok Build 1.0.70 sends a failed model request fifteen more times, with a backoff
 * that starts at 2 s and doubles to 30 s, and then retries the whole step three
 * times. That exceeds the test budget, so this agent opens with the sampler retries
 * off (`[models] max_retries = 0`). The step retries remain.
 */
const GROK_STEP_ATTEMPTS = 4

grokTest('shows the native model failure and accepts the next valid prompt', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running' })
})

grokTest('folds a retried mid-stream cut into one retry row', async ({ native }) => {
  const { page, modelScript } = native
  const start = await modelScript.queue(
    {
      error: { status: 503, message: 'cut mid-stream', midStream: true },
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

grokTest('schedules LeapMux\'s own server-error retry after the native client gives up', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const configPath = join(leapmuxServer.agentEnv.GROK_HOME!, 'config.toml')
  const config = readFileSync(configPath, 'utf8')
  if (!config.includes('\n[models]\n'))
    throw new Error('The Grok configuration has no [models] table to hold the retry count.')
  const content = config.replace('\n[models]\n', '\n[models]\nmax_retries = 0\n')
  await withNativeConfigurationFile({ path: configPath, content, runDir: getGlobalState().tmpDir }, async () => {
    const context = await nativeContext({
      page,
      modelScript,
      leapmuxServer,
      workspaceId: authenticatedEmptyWorkspace.workspaceId,
    })
    await openNativeAgent(context, { directoryPrefix: 'grok-server-error-' })
    // Grok fails the prompt with -32603 and the HTTP status of the last attempt.
    await exerciseTransientModelFailure(context, {
      error: { status: 503, code: 'server_error', message: 'Service temporarily unavailable.' },
      attempts: GROK_STEP_ATTEMPTS,
    })
  })
})
