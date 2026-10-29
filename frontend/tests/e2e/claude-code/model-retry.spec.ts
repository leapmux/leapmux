import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { claudeTest } from '../claude-fixtures'
import { openNativeAgent } from '../helpers/nativeAgentOpen'
import { withNativeConfigurationFile } from '../helpers/nativeConfigurationFile'
import { exerciseTransientModelFailure } from '../helpers/nativeModelError'
import { getGlobalState } from '../helpers/server'
import { assistantBubbles, messageContents, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { nativeContext } from './scenarios'

/**
 * How many times Claude Code retries a failed request in the transient case.
 *
 * Claude Code 2.1.310 retries an overloaded request ten times by default, with a backoff that doubles to 32 s, and a
 * `retry-after` header of one second on a 529 does not shorten it: nine requests took 110 s. That exceeds the test
 * budget, so that agent opens with Claude Code's own setting `CLAUDE_CODE_MAX_RETRIES` lowered, through the `env` of
 * its user settings.
 */
const CLAUDE_MAX_RETRIES = 2

claudeTest('folds a retried request into one resolved retry row', async ({ native }) => {
  const { page, modelScript } = native
  const start = await modelScript.queue(
    {
      error: { status: 529, message: 'overloaded' },
    },
    { text: 'RETRIED_OK' },
  )
  await sendMessage(page, modelScript.prompt('Reply after the request succeeds.'))
  await modelScript.waitForSteps(start + 2)
  await waitForAgentIdle(page)
  for (const reload of [false, true]) {
    if (reload) {
      await page.reload()
      await waitForAgentIdle(page)
    }
    const retries = messageContents(page).filter({ hasText: 'API retry' })
    await expect(retries).toHaveCount(1)
    await expect(retries).toContainText('succeeded')
    await expect(assistantBubbles(page).filter({ hasText: 'RETRIED_OK' })).toHaveCount(1)
  }
})

claudeTest('schedules LeapMux\'s own server-error retry after the native client gives up', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const settingsPath = join(leapmuxServer.agentEnv.CLAUDE_CONFIG_DIR!, 'settings.json')
  const settings = JSON.parse(readFileSync(settingsPath, 'utf8'))
  const content = `${JSON.stringify({
    ...settings,
    env: {
      ...settings.env,
      CLAUDE_CODE_MAX_RETRIES: String(CLAUDE_MAX_RETRIES),
    },
  })}\n`
  await withNativeConfigurationFile({ path: settingsPath, content, runDir: getGlobalState().tmpDir }, async () => {
    const context = await nativeContext({
      page,
      modelScript,
      leapmuxServer,
      workspaceId: authenticatedEmptyWorkspace.workspaceId,
    })
    await openNativeAgent(context, { directoryPrefix: 'claude-server-error-' })
    // Claude Code states the last 529 as "API Error: ... overloaded_error ...", which LeapMux retries.
    await exerciseTransientModelFailure(context, {
      error: { status: 529, code: 'overloaded_error', message: 'Overloaded' },
      attempts: CLAUDE_MAX_RETRIES + 1,
    })
  })
})
