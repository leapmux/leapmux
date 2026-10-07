import { expect } from '@playwright/test'
import { exerciseBasicChat } from '../helpers/nativeConversation'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, messageContents, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { piTest } from '../pi-fixtures'

piTest('ends a native turn with a timed divider, hides agent_settled, and keeps its answer after reload', async ({ native, page, modelScript }) => {
  await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
  await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
  await modelScript.waitForSteps()
  await waitForAgentIdle(page)

  // Pi emits agent_settled after agent_end. The Worker drops it, so the chat
  // must never draw it as a raw JSON bubble.
  //
  // Count the rows first. An absence assertion over an empty locator passes
  // without a row to test, so this check fails when a change of the message
  // list makes the locator match no row.
  const contents = messageContents(page)
  expect(await contents.count()).toBeGreaterThan(0)
  const allText = (await contents.allTextContents()).join(' ')
  expect(allText).not.toContain('agent_settled')

  // Pi's agent_end carries no duration. The Worker measures the turn and adds
  // `duration_ms` to the turn end, so the divider always states a time.
  await exerciseBasicChat(native, { timedDivider: true })
})
