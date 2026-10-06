import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseBasicChat } from '../helpers/nativeConversation'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, expectAssistantAnswer, sendMessage, waitForAgentIdle, waitForWorkspaceReady } from '../helpers/ui'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest('agent starts and shows ready state', async ({ authenticatedOpencodeWorkspace, page, modelScript }) => {
  void authenticatedOpencodeWorkspace // fixture trigger

  // The editor renders regardless of agent state — a composer-editor visibility
  // check alone passes even when the agent backend is broken. Send a
  // trivial prompt and assert a response comes back so the test catches
  // a regression where the agent fails to start.
  //
  // expectAssistantAnswer, not lastAssistantBubble: a turn-end divider is an
  // agent-role bubble too, so the LAST one is the divider whenever it lands
  // after the reply, and its own "turn completed" text satisfies a bare
  // length check -- which is precisely the "agent fails to start" regression
  // this test exists to catch.
  await modelScript.queue({ text: 'ready' })
  await sendMessage(page, modelScript.prompt('Reply with just the word: ready'))
  await modelScript.waitForSteps()
  await waitForAgentIdle(page)
  await expectAssistantAnswer(page, { answer: /ready/i })
})

opencodeTest('agent reconnects after page reload', async ({ authenticatedOpencodeWorkspace, page, modelScript }) => {
  void authenticatedOpencodeWorkspace // fixture trigger

  // Reload the page, then verify a fresh prompt is processed by the
  // reconnected agent — proves reconnection, not just a re-rendered shell.
  await page.reload()
  await waitForWorkspaceReady(page)

  await modelScript.queue({ text: 'hello' })
  await sendMessage(page, modelScript.prompt('Reply with just the word: hello'))
  await modelScript.waitForSteps()
  await waitForAgentIdle(page)
  await expectAssistantAnswer(page, { answer: /hello/i })
})

opencodeTest('renders an assistant answer and clears the thinking indicator', async ({ authenticatedOpencodeWorkspace, page, modelScript }) => {
  void authenticatedOpencodeWorkspace
  await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
  await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
  await waitForAgentIdle(page)
  await expectAssistantAnswer(page)
  await expect(page.getByTestId('thinking-indicator')).not.toBeVisible()
})

opencodeTest('ends a native turn and keeps its answer after reload', async ({ authenticatedOpencodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedOpencodeWorkspace.workspaceId, provider: AgentProvider.OPENCODE }
  await exerciseBasicChat(context)
})
