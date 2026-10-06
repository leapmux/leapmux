import { expect } from '@playwright/test'

import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, bandRows, expectAssistantAnswer, sendMessage, waitForAgentIdle } from '../helpers/ui'

import { kimiTest } from '../kimi-fixtures'

kimiTest.describe('uses Kimi Code for basic chat', () => {
  // The worker assembles the streamed reasoning and text into rows of its own,
  // so a reload reads the same rows that the live turn drew.
  kimiTest('draws the reasoning before the answer and keeps both after a reload', async ({ authenticatedKimiWorkspace, page, modelScript }) => {
    void authenticatedKimiWorkspace
    const reasoning = 'I add the two numbers column by column.'
    await modelScript.queue({ reasoning, text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expectAssistantAnswer(page)
    await expect(bandRows(page, 'thought').filter({ hasText: reasoning }).first()).toBeVisible()
    await expect(bandRows(page, 'text').filter({ hasText: reasoning })).toHaveCount(0)

    await page.reload()
    await expectAssistantAnswer(page)
    await expect(bandRows(page, 'thought').filter({ hasText: reasoning }).first()).toBeVisible()
    await expect(bandRows(page, 'text').filter({ hasText: reasoning })).toHaveCount(0)
  })
})
