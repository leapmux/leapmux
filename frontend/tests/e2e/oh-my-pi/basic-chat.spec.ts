import { expect } from '@playwright/test'
import { exerciseBasicChat } from '../helpers/nativeConversation'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, expectAssistantAnswer, messageContents, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { ohMyPiTest } from '../ohmypi-fixtures'

/**
 * The native agent reads the prompt and returns the answer.
 * Its completion event ends the browser turn.
 *
 * The Worker drives `omp --mode rpc-ui` through its JSON Lines protocol.
 */
ohMyPiTest.describe('Oh My Pi basic chat', () => {
  ohMyPiTest('renders an assistant answer and ends the turn with a timed divider', async ({ native, page, modelScript }) => {
    await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expectAssistantAnswer(page)
    // Count the rows first.
    // An absence assertion over an empty locator can pass without testing a row.
    const contents = messageContents(page)
    expect(await contents.count()).toBeGreaterThan(0)
    // The Worker drops token updates and turn frames.
    // The chat must show no raw protocol rows.
    const allText = (await contents.allTextContents()).join(' ')
    expect(allText).not.toContain('message_update')
    expect(allText).not.toContain('turn_end')
    // The native `agent_end` event states no duration. The Worker measures the turn and adds the duration to the turn
    // end.
    await exerciseBasicChat(native, { timedDivider: true })
  })
})
