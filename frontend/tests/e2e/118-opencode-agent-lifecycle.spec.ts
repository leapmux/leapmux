import { lastAssistantBubble, sendMessage, waitForAgentIdle, waitForWorkspaceReady } from './helpers/ui'
import { expect, OPENCODE_E2E_SKIP_REASON, opencodeTest } from './opencode-fixtures'

opencodeTest.skip(!!OPENCODE_E2E_SKIP_REASON, OPENCODE_E2E_SKIP_REASON || '')

opencodeTest.describe('OpenCode Agent Lifecycle', () => {
  opencodeTest('agent starts and shows ready state', async ({ authenticatedOpencodeWorkspace, page }) => {
    void authenticatedOpencodeWorkspace // fixture trigger

    // The editor renders regardless of agent state — a chat-editor visibility
    // check alone passes even when the agent backend is broken. Send a
    // trivial prompt and assert a response comes back so the test catches
    // a regression where the agent fails to start.
    await sendMessage(page, 'Reply with just the word: ready')
    await waitForAgentIdle(page, 120_000)
    const bubble = await lastAssistantBubble(page)
    await expect(bubble).toBeVisible()
    expect((await bubble.textContent() ?? '').length).toBeGreaterThan(0)
  })

  opencodeTest('agent reconnects after page reload', async ({ authenticatedOpencodeWorkspace, page }) => {
    void authenticatedOpencodeWorkspace // fixture trigger

    // Reload the page, then verify a fresh prompt is processed by the
    // reconnected agent — proves reconnection, not just a re-rendered shell.
    await page.reload()
    await waitForWorkspaceReady(page)

    await sendMessage(page, 'Reply with just the word: hello')
    await waitForAgentIdle(page, 120_000)
    const bubble = await lastAssistantBubble(page)
    await expect(bubble).toBeVisible()
    expect((await bubble.textContent() ?? '').length).toBeGreaterThan(0)
  })
})
