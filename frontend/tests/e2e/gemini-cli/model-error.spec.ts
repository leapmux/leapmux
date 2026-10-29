import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { geminiTest } from '../gemini-fixtures'
import { openNativeAgent } from '../helpers/nativeAgentOpen'
import { withNativeConfigurationFile } from '../helpers/nativeConfigurationFile'
import { exerciseModelError, exerciseTransientModelFailure } from '../helpers/nativeModelError'
import { getGlobalState } from '../helpers/server'
import { nativeContext } from './scenarios'

/**
 * How many times this agent sends the request of one turn.
 *
 * Gemini CLI 0.65.0 sends a 5xx ten times by default (`general.maxAttempts`), with a
 * backoff that starts at 5 s and grows to 30 s, about 215 s in all. That exceeds the
 * test budget, so this agent opens with the setting lowered to two attempts.
 */
const GEMINI_MAX_ATTEMPTS = 2

geminiTest('shows the native model failure and accepts the next valid prompt', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running' })
})

geminiTest('schedules LeapMux\'s own server-error retry after the native client gives up', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const settingsPath = join(leapmuxServer.agentEnv.GEMINI_CLI_HOME!, '.gemini', 'settings.json')
  const settings = JSON.parse(readFileSync(settingsPath, 'utf8'))
  const content = `${JSON.stringify({
    ...settings,
    general: { ...settings.general, maxAttempts: GEMINI_MAX_ATTEMPTS },
  }, null, 2)}\n`
  await withNativeConfigurationFile({ path: settingsPath, content, runDir: getGlobalState().tmpDir }, async () => {
    const context = await nativeContext({
      page,
      modelScript,
      leapmuxServer,
      workspaceId: authenticatedEmptyWorkspace.workspaceId,
    })
    await openNativeAgent(context, { directoryPrefix: 'gemini-server-error-' })
    // Gemini CLI fails the prompt with the HTTP status as its JSON-RPC code.
    await exerciseTransientModelFailure(context, {
      error: { status: 503, code: 'UNAVAILABLE', message: 'The model is overloaded. Please try again later.' },
      attempts: GEMINI_MAX_ATTEMPTS,
    })
  })
})

geminiTest('stops a turn whose native retry waits for hours and schedules LeapMux\'s own retry', async ({ native }) => {
  // Gemini CLI 0.65.0 reads an exhausted capacity whose message states a delay of
  // more than five minutes as a capacity limit, and waits out that delay inside the
  // open turn, here for about 19 hours. It states the wait only in a warning in
  // its debug log. LeapMux ends the turn, and schedules its own retry for that
  // instant, which a restart keeps.
  await exerciseTransientModelFailure(native, {
    error: { status: 429, code: 'RESOURCE_EXHAUSTED', message: 'You have exhausted your capacity on this model. Please retry in 69287s.' },
    attempts: 1,
    retryKind: 'usage limit',
  })
})
