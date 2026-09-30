import { expect, GROK_E2E_SKIP_REASON, grokTest } from './grok-fixtures'
import { exerciseContextUsage } from './helpers/contextUsage'
import { exerciseManualCompaction } from './helpers/manualCompaction'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, bandRows, expectAssistantAnswer, sendMessage, waitForAgentIdle } from './helpers/ui'

grokTest.skip(!!GROK_E2E_SKIP_REASON, GROK_E2E_SKIP_REASON || '')

grokTest.describe('Grok Build Basic Chat', () => {
  grokTest('draws model reasoning in a thought row', async ({ authenticatedGrokWorkspace, page, modelScript }) => {
    void authenticatedGrokWorkspace
    await modelScript.queue({ reasoning: 'I inspect the numbers first.', text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(bandRows(page, 'thought').filter({ hasText: 'I inspect the numbers first.' }).first()).toBeVisible()
    await expectAssistantAnswer(page)
  })

  grokTest('reports model usage in the agent info card', async ({ authenticatedGrokWorkspace, page, modelScript }) => {
    void authenticatedGrokWorkspace
    await exerciseContextUsage(page, modelScript)
  })

  grokTest('send message and receive response', async ({ authenticatedGrokWorkspace, page, modelScript }) => {
    void authenticatedGrokWorkspace
    await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await waitForAgentIdle(page)
    await expectAssistantAnswer(page)
  })

  grokTest('compacts a scripted conversation on request', async ({ authenticatedGrokWorkspace, page, modelScript }) => {
    void authenticatedGrokWorkspace
    await exerciseManualCompaction(page, modelScript, { summaryRequestMarker: 'Your task is to produce a faithful, concise summary of the conversation so far' })
  })
})
