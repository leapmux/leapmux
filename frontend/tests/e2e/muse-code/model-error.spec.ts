import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { openNativeAgent } from '../helpers/nativeAgentOpen'
import { withNativeConfigurationFile } from '../helpers/nativeConfigurationFile'
import { exerciseModelError, exerciseTransientModelFailure } from '../helpers/nativeModelError'
import { exerciseModelRetry } from '../helpers/nativeModelRetry'
import { getGlobalState } from '../helpers/server'
import { museTest } from '../muse-fixtures'
import { nativeContext } from './scenarios'

museTest('shows the native model error and accepts the next valid turn', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running' })
})

museTest('marks the cut attempt and folds the retry', async ({ native }) => {
  await exerciseModelRetry(native, {
    errors: [
      {
        status: 503,
        message: 'The response ended early.',
        code: 'server_error',
        midStream: 'truncate',
      },
    ],
    failedAttempt: 'error',
  })
})

museTest('schedules LeapMux\'s own server-error retry after the native client gives up', async ({ native }) => {
  // The E2E settings give Muse no provider retry (`provider_retry` in
  // `helpers/museEnvironment.ts`), so the 500 ends the turn after one request, as
  // a `modelError` that Muse marks retryable.
  await exerciseTransientModelFailure(native, {
    error: { status: 500, code: 'server_error', message: 'Internal Server Error' },
    attempts: 1,
  })
})

museTest('shows a provider retry of Muse as a resolved retry row', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  // Muse retries a 503 at the provider level and states each retry as
  // `turn/modelRetryScheduled`. The run's settings turn that retry off, so this
  // agent opens with one provider retry.
  const settingsPath = join(leapmuxServer.agentEnv.HOME!, '.config', 'muse', 'settings.json')
  const settings = JSON.parse(readFileSync(settingsPath, 'utf8'))
  const content = `${JSON.stringify({
    ...settings,
    provider_retry: { ...settings.provider_retry, max_retries: 1 },
  }, null, 2)}\n`
  await withNativeConfigurationFile({ path: settingsPath, content, runDir: getGlobalState().tmpDir }, async () => {
    const context = await nativeContext({
      page,
      modelScript,
      leapmuxServer,
      workspaceId: authenticatedEmptyWorkspace.workspaceId,
    })
    await openNativeAgent(context, {
      directoryPrefix: 'muse-provider-retry-',
      overrides: {
        optionValues: { permissionMode: 'allowAll' },
      },
    })
    await exerciseModelRetry(context, {
      errors: [
        { status: 503, code: 'server_error', message: 'Service Unavailable' },
      ],
    })
  })
})
