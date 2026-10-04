import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseBasicChat } from '../helpers/nativeConversation'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, expectAssistantAnswer, messageContents, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { PI_E2E_SKIP_REASON, piTest } from '../pi-fixtures'

piTest.skip(!!PI_E2E_SKIP_REASON, PI_E2E_SKIP_REASON || '')

piTest('renders an assistant answer and clears the thinking indicator', async ({ authenticatedPiWorkspace, page, modelScript }) => {
  void authenticatedPiWorkspace
  await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
  await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
  await waitForAgentIdle(page, 180_000)
  await expectAssistantAnswer(page)
  await expect(page.getByTestId('thinking-indicator')).not.toBeVisible()
})

piTest('Pi agent tab is visible after creation', async ({ authenticatedPiWorkspace, page }) => {
  void authenticatedPiWorkspace // fixture trigger
  const tabs = page.locator('[data-testid="tab"]')
  await expect(tabs.first()).toBeVisible()
})

piTest('turn-end divider reports the duration, and agent_settled stays hidden', async ({ authenticatedPiWorkspace, page, modelScript }) => {
  void authenticatedPiWorkspace // fixture trigger
  await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
  await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
  await modelScript.waitForSteps()
  await waitForAgentIdle(page, 180_000)

  // Pi's agent_end carries no duration; the worker measures the turn and
  // injects duration_ms, so the divider always names a time.
  // `:visible` scoping is required — ChatView renders every unmeasured row
  // twice, so an unscoped locator can pick the offscreen copy.
  const divider = page.locator('[data-testid="result-divider"]:visible').last()
  await expect(divider).toHaveText(/^Turn ended \(.+\)$/)

  // Pi emits agent_settled after agent_end. The worker drops it, so it must
  // never surface as a raw-JSON bubble.
  //
  // Count the rows first. An absence assertion over an empty locator passes
  // for the wrong reason, so this fails loudly if the message list is ever
  // renamed out from under the helper.
  const contents = messageContents(page)
  expect(await contents.count()).toBeGreaterThan(0)
  const allText = (await contents.allTextContents()).join(' ')
  expect(allText).not.toContain('agent_settled')
})

piTest('ends a native turn and keeps its answer after reload', async ({ authenticatedPiWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedPiWorkspace.workspaceId, provider: AgentProvider.PI }
  await exerciseBasicChat(context)
})
