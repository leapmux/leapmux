import { exerciseContextUsage } from './helpers/contextUsage'
import { exerciseManualCompaction, MANUAL_COMPACTION_SUMMARY } from './helpers/manualCompaction'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, bandRows, expectAssistantAnswer, sendMessage, waitForAgentIdle } from './helpers/ui'
import { expect, QWEN_E2E_SKIP_REASON, qwenTest } from './qwen-fixtures'

qwenTest.skip(!!QWEN_E2E_SKIP_REASON, QWEN_E2E_SKIP_REASON || '')

qwenTest.describe('Qwen Code Basic Chat', () => {
  qwenTest('draws model reasoning in a thought row', async ({ authenticatedQwenWorkspace, page, modelScript }) => {
    void authenticatedQwenWorkspace
    await modelScript.queue({ reasoning: 'I inspect the numbers first.', text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(bandRows(page, 'thought').filter({ hasText: 'I inspect the numbers first.' }).first()).toBeVisible()
    await expectAssistantAnswer(page)
  })

  qwenTest('reports model usage in the agent info card', async ({ authenticatedQwenWorkspace, page, modelScript }) => {
    void authenticatedQwenWorkspace
    await exerciseContextUsage(page, modelScript)
  })

  qwenTest('send message and receive response', async ({ authenticatedQwenWorkspace, page, modelScript }) => {
    void authenticatedQwenWorkspace
    await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await waitForAgentIdle(page)
    await expectAssistantAnswer(page)
  })

  qwenTest('compacts a scripted conversation on request', async ({ authenticatedQwenWorkspace, page, modelScript }) => {
    void authenticatedQwenWorkspace
    const summary = `<analysis>The conversation needs a checkpoint.</analysis><state_snapshot><current_work>${MANUAL_COMPACTION_SUMMARY}</current_work><next_step>Continue the user's work.</next_step></state_snapshot>`
    await exerciseManualCompaction(page, modelScript, {
      summary,
      reportedInputTokens: 10_000,
      summaryRequestMarker: 'You are the component that summarizes a conversation',
    })
  })
})
