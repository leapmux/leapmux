import { CLAUDE_AGENT, claudeTest } from '../claude-fixtures'
import { exerciseNativeQuotaHeaders } from '../helpers/nativeQuota'
import { withNativeWorker } from '../helpers/nativeWorker'
import { exerciseRateLimitWindow, nearLimitRateLimits } from '../helpers/rateLimit'
import { createTestDirectory } from '../helpers/runDirectory'
import { loginViaToken, openWorkspace } from '../helpers/ui'
import { expectNoRateLimitState } from '../helpers/unsupportedRateLimit'
import { openProviderAgent, withTestWorkspace } from '../helpers/workspace'
import { nativeContext } from './scenarios'

claudeTest.describe('Claude Code rate-limit state', () => {
  claudeTest('a subscriber sees the model warning after a reload', async ({ page, leapmuxServer, modelScript }) => {
    const token = leapmuxServer.agentEnv.LEAPMUX_E2E_MODEL_API_KEY
    if (!token)
      throw new Error('The mock Claude subscriber needs the isolated model token.')
    // A subscriber signs in with an OAuth token alone. The private Worker removes each API key and each cloud
    // provider of the suite environment, so the CLI uses the subscriber path that reports the rate-limit state.
    await withNativeWorker(leapmuxServer, {
      dataDirPrefix: 'claude-subscriber-worker',
      workerName: 'Claude subscriber test',
      env: {
        ANTHROPIC_API_KEY: undefined,
        ANTHROPIC_AUTH_TOKEN: undefined,
        CLAUDE_CODE_API_KEY_FILE_DESCRIPTOR: undefined,
        CLAUDE_CODE_USE_BEDROCK: undefined,
        CLAUDE_CODE_USE_VERTEX: undefined,
        CLAUDE_CODE_USE_FOUNDRY: undefined,
        CLAUDE_CODE_OAUTH_TOKEN: token,
      },
    }, async ({ server }) => {
      await withTestWorkspace(server, 'claude-rate-limit', async ({ workspaceId }) => {
        await openProviderAgent(server, workspaceId, CLAUDE_AGENT, {
          workingDir: createTestDirectory('claude-subscriber-wd-'),
          optionValues: { permissionMode: 'default' },
        })
        await loginViaToken(page, server.adminToken)
        await openWorkspace(page, workspaceId)
        const context = await nativeContext({ page, modelScript, leapmuxServer: server, workspaceId })
        await exerciseRateLimitWindow(context, nearLimitRateLimits(), { reload: true })
      })
    })
  })

  claudeTest('the API-key headers do not create subscriber rate-limit state', async ({ native }) => {
    await expectNoRateLimitState(native, { relatedProof: () => exerciseNativeQuotaHeaders(native) })
  })
})
