import { expect } from '@playwright/test'
import { exerciseBasicChat } from '../helpers/nativeConversation'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, messageContents, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { piTest } from '../pi-fixtures'

piTest('turn-end divider reports the duration, and agent_settled stays hidden', async ({ authenticatedPiWorkspace, page, modelScript }) => {
  void authenticatedPiWorkspace // fixture trigger
  await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
  await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
  await modelScript.waitForSteps()
  await waitForAgentIdle(page)

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

piTest('ends a native turn and keeps its answer after reload', async ({ native }) => {
  await exerciseBasicChat(native)
})
