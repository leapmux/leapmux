import { expect } from '@playwright/test'
import { ampTest } from '../amp-fixtures'
import { exerciseBasicChat } from '../helpers/nativeConversation'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, bandRows, expectAssistantAnswer, messageContents, sendMessage, waitForAgentIdle } from '../helpers/ui'

/**
 * The native agent reads the prompt and returns the answer.
 * Its completion event ends the browser turn.
 *
 * The Worker drives Amp's stream JSON protocol.
 * The isolated mock supplies Amp's remote service.
 */
ampTest.describe('Amp basic chat', () => {
  ampTest('renders an assistant answer and ends the turn with a timed divider', async ({ native, page, modelScript }) => {
    await modelScript.queue({ reasoning: 'Add the two numbers.', text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expectAssistantAnswer(page)
    await expect(bandRows(page, 'thought').filter({ hasText: 'Add the two numbers.' }).first()).toBeVisible()
    // Amp reports no turn duration.
    // The Worker measures the turn duration and includes it in the divider.
    await expect(page.locator('[data-testid="result-divider"]:visible').last()).toHaveText(/^Turn ended \(.+\)$/)
    // Count the rows first.
    // An absence assertion over an empty locator can pass without testing a row.
    const contents = messageContents(page)
    expect(await contents.count()).toBeGreaterThan(0)
    // The Worker drops Amp's prompt echo and init line.
    // The chat must show no raw protocol rows.
    const allText = (await contents.allTextContents()).join(' ')
    expect(allText).not.toContain('"subtype":"init"')
    expect(allText).not.toContain('stream-json')
    await exerciseBasicChat(native)
  })
})
