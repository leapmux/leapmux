import type { WebSocketRoute } from '@playwright/test'
import { expect } from '@playwright/test'
import { WS_CHANNEL_ROUTE } from '../../src/generated/contracts/wire'
import { test } from './fixtures'
import { withExtraClients } from './helpers/multiClient'
import { expectToastRecorded } from './helpers/toast'
import { assistantBubbles, composerEditor, fillComposer, loginViaToken, openWorkspace, PLATFORM_MOD, sendMessage, userBubbles, waitForAgentIdle } from './helpers/ui'

test('sends identical text as a new message after recovery shows the first one was delivered', async ({ page, browser, authenticatedWorkspace, leapmuxServer, modelScript }) => {
  const warmupAnswer = 'The agent is ready for the resend test.'
  await modelScript.queue({ text: warmupAnswer })
  await sendMessage(page, modelScript.prompt('Confirm the agent is ready.'))
  await expect(assistantBubbles(page).filter({ hasText: warmupAnswer })).toHaveCount(1)
  await waitForAgentIdle(page)

  await withExtraClients(browser, leapmuxServer, 1, async ([sender]) => {
    const routes = new Set<WebSocketRoute>()
    let withholdReplies = false
    await sender.routeWebSocket(url => url.pathname === WS_CHANNEL_ROUTE, (client) => {
      routes.add(client)
      const server = client.connectToServer()
      server.onMessage((message) => {
        if (!withholdReplies)
          client.send(message)
      })
    })
    await loginViaToken(sender, leapmuxServer.adminToken)
    await openWorkspace(sender, authenticatedWorkspace.workspaceId)
    await expect(assistantBubbles(sender).filter({ hasText: warmupAnswer })).toHaveCount(1)
    await waitForAgentIdle(sender)
    expect(routes.size).toBeGreaterThan(0)

    const question = 'Review the same input twice.'
    const prompt = modelScript.prompt(question)
    const firstAnswer = 'The first input reached the model.'
    await modelScript.queue({ text: firstAnswer })
    await fillComposer(sender, prompt)
    withholdReplies = true
    await sender.keyboard.press(`${PLATFORM_MOD}+Enter`)

    // The other browser proves acceptance while the sender receives neither
    // the enqueue response nor a queue snapshot or transcript update.
    await expect(userBubbles(page).filter({ hasText: question })).toHaveCount(1)
    await expect(assistantBubbles(page).filter({ hasText: firstAnswer })).toHaveCount(1)
    await expect(userBubbles(sender).filter({ hasText: question })).toHaveCount(0)
    const oldRoutes = [...routes]
    withholdReplies = false
    await Promise.all(oldRoutes.map(client => client.close({ code: 1012, reason: 'The test discarded the enqueue response' })))
    await expectToastRecorded(sender, 'channel disconnected')
    await expect(userBubbles(sender).filter({ hasText: question })).toHaveCount(1)
    // Playwright reads textContent, which omits the editor's paragraph breaks.
    await expect(composerEditor(sender)).toHaveText(prompt.replaceAll('\n', ''))
    await waitForAgentIdle(page)

    const secondAnswer = 'The repeated text reached the model as a new input.'
    await modelScript.queue({ text: secondAnswer })
    await composerEditor(sender).press(`${PLATFORM_MOD}+Enter`)
    await expect(composerEditor(sender)).toHaveText('')
    await expect(userBubbles(page).filter({ hasText: question })).toHaveCount(2)
    await expect(assistantBubbles(page).filter({ hasText: secondAnswer })).toHaveCount(1)
  })
})
