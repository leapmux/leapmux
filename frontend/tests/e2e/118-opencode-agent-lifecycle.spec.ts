import { expectAssistantAnswer, sendMessage, waitForAgentIdle, waitForWorkspaceReady } from './helpers/ui'
import { OPENCODE_E2E_SKIP_REASON, opencodeTest } from './opencode-fixtures'

opencodeTest.skip(!!OPENCODE_E2E_SKIP_REASON, OPENCODE_E2E_SKIP_REASON || '')

opencodeTest.describe('OpenCode Agent Lifecycle', () => {
  opencodeTest('agent starts and shows ready state', async ({ authenticatedOpencodeWorkspace, page }) => {
    void authenticatedOpencodeWorkspace // fixture trigger

    // The editor renders regardless of agent state — a chat-editor visibility
    // check alone passes even when the agent backend is broken. Send a
    // trivial prompt and assert a response comes back so the test catches
    // a regression where the agent fails to start.
    //
    // expectAssistantAnswer, not lastAssistantBubble: a turn-end divider is an
    // agent-role bubble too, so the LAST one is the divider whenever it lands
    // after the reply, and its own "turn completed" text satisfies a bare
    // length check -- which is precisely the "agent fails to start" regression
    // this test exists to catch.
    await sendMessage(page, 'Reply with just the word: ready')
    await waitForAgentIdle(page, 120_000)
    await expectAssistantAnswer(page, { answer: /ready/i })
  })

  opencodeTest('agent reconnects after page reload', async ({ authenticatedOpencodeWorkspace, page }) => {
    void authenticatedOpencodeWorkspace // fixture trigger

    // Reload the page, then verify a fresh prompt is processed by the
    // reconnected agent — proves reconnection, not just a re-rendered shell.
    await page.reload()
    await waitForWorkspaceReady(page)

    await sendMessage(page, 'Reply with just the word: hello')
    await waitForAgentIdle(page, 120_000)
    await expectAssistantAnswer(page, { answer: /hello/i })
  })
})
