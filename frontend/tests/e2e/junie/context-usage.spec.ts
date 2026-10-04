import type { ModelScript } from '../helpers/modelScriptFixture'
import { expectContextUsage } from '../helpers/contextUsage'
import { junieAnswerToolCall } from '../helpers/providerToolCalls'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { JUNIE_E2E_SKIP_REASON, junieTest } from '../junie-fixtures'

junieTest.describe('Junie attachments and context usage', () => {
  junieTest.skip(!!JUNIE_E2E_SKIP_REASON, JUNIE_E2E_SKIP_REASON || '')

  async function scriptJunieAttachmentHousekeeping(modelScript: ModelScript, title: string): Promise<void> {
    await modelScript.rule(
      { name: 'junie-attachment-capability-filter', when: { system: 'capability filter agent' }, respond: { text: '' } },
      { name: 'junie-attachment-task-name', when: { system: 'task description summarizer' }, respond: { text: title } },
    )
  }

  junieTest('the agent info grid follows the usage the model reports', async ({ authenticatedJunieWorkspace, page, modelScript }) => {
    void authenticatedJunieWorkspace
    await scriptJunieAttachmentHousekeeping(modelScript, 'Usage task')
    const usage = { inputTokens: 12000, outputTokens: 40 }
    await modelScript.queue({
      toolCalls: [junieAnswerToolCall('junie-usage-answer', 'Usage recorded.')],
      usage,
    })
    await sendMessage(page, modelScript.prompt('Reply once.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expectContextUsage(page, usage)
  })
})
