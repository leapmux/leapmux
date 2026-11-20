import type { Page, WebSocketRoute } from '@playwright/test'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { WS_CHANNEL_ROUTE } from '../../../src/generated/contracts/wire'
import { agentOpenOptions } from '../agentSettings'
import { test } from '../fixtures'
import { openAgentViaAPI } from '../helpers/api'
import { withExtraClients } from '../helpers/multiClient'
import { nativeStartupShellEnvironment } from '../helpers/nativeStartupWorker'
import { withPrivateNativeWorkspace } from '../helpers/privateNativeWorkspace'
import { hubSpawnEnv } from '../helpers/server'
import { loginViaToken, messageBubbles, openWorkspace, sendMessage, toolCallRow, waitForWorkspaceReady } from '../helpers/ui'
import { prepareRefusalFixture, REFUSAL_NOTICE, REFUSAL_PROMPT, REFUSED_TEXT, REFUSED_TOOL_CALL, REPLACEMENT_TEXT } from './refusalFixture'
import { CLAUDE_AGENT } from './scenarios'

async function expectRefusedRows(page: Page, count: 0 | 1): Promise<void> {
  for (const text of REFUSED_TEXT)
    await expect(messageBubbles(page).filter({ hasText: text })).toHaveCount(count)
  await expect(toolCallRow(page, REFUSED_TOOL_CALL)).toHaveCount(count)
}

async function expectSurvivingRows(page: Page): Promise<void> {
  await expect(messageBubbles(page).filter({ hasText: REFUSAL_PROMPT })).toHaveCount(1)
  await expect(messageBubbles(page).filter({ hasText: REFUSAL_NOTICE })).toHaveCount(1)
  await expect(messageBubbles(page).filter({ hasText: REPLACEMENT_TEXT }).first()).toBeVisible()
  await expect(toolCallRow(page, REFUSED_TOOL_CALL, 'request')).toBeVisible()
}

test('removes exact Claude refusal rows live, after reconnect, and after reload', async ({ page, browser, leapmuxServer }) => {
  await withPrivateNativeWorkspace(page, leapmuxServer, {
    prefix: 'claude-refusal',
    workerName: 'Claude refusal fixture',
    providerAgent: CLAUDE_AGENT,
    prepare: directory => ({
      env: nativeStartupShellEnvironment(join(directory, 'shell'), directory, hubSpawnEnv(leapmuxServer.agentEnv)),
      setup: prepareRefusalFixture(directory),
    }),
    openAgent: (server, workspaceId, workingDir) => openAgentViaAPI(server, workspaceId, workingDir, agentOpenOptions(CLAUDE_AGENT.provider)),
  }, async ({ server, workspaceId, setup }) => {
    await withExtraClients(browser, server, 1, async ([stale]) => {
      let offline = false
      let relay: WebSocketRoute | undefined
      let connectedRelays = 0
      await stale.routeWebSocket(url => url.pathname === WS_CHANNEL_ROUTE, async (route) => {
        if (offline) {
          await route.close({ code: 1012, reason: 'Refusal fixture disconnected the relay' })
          return
        }
        route.connectToServer()
        relay = route
        connectedRelays++
      })
      await loginViaToken(stale, server.adminToken)
      await openWorkspace(stale, workspaceId)
      await sendMessage(page, REFUSAL_PROMPT)
      await expectRefusedRows(page, 1)
      await expectRefusedRows(stale, 1)
      await expect(toolCallRow(page, REFUSED_TOOL_CALL, 'request')).toBeVisible()
      if (!relay)
        throw new Error('The second view must own an encrypted relay before the fixture releases the evictions.')

      offline = true
      const initialConnection = connectedRelays
      await relay.close({ code: 1012, reason: 'Refusal fixture disconnected the relay' })
      setup.release()
      await expectSurvivingRows(page)
      await expectRefusedRows(page, 0)
      await expectRefusedRows(stale, 1)
      await expect(messageBubbles(stale).filter({ hasText: REFUSAL_NOTICE })).toHaveCount(0)

      offline = false
      await expect.poll(() => connectedRelays).toBeGreaterThan(initialConnection)
      await expectSurvivingRows(stale)
      await expectRefusedRows(stale, 0)

      await stale.reload()
      await waitForWorkspaceReady(stale)
      await expectSurvivingRows(stale)
      await expectRefusedRows(stale, 0)
    })
  })
})
