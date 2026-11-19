import { expect } from '@playwright/test'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { exerciseModelError } from '../helpers/nativeModelError'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { assistantBubbles, messageContents, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest('shows the native model failure and accepts a later valid prompt', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running' })
})

opencodeTest('keeps only the retried answer after a mid-stream failure', async ({ native }, testInfo) => {
  const { page, modelScript } = native
  const agent = await currentNativeAgent(native)
  try {
    const start = await modelScript.queue(
      {
        error: { status: 503, message: 'Service unavailable', midStream: true },
      },
      { text: 'FULL-ANSWER-AFTER-RETRY' },
    )
    await sendMessage(page, modelScript.prompt('Stream the scripted answer.'))
    await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)
    for (const reload of [false, true]) {
      if (reload)
        await page.reload()
      await expect(assistantBubbles(page).filter({ hasText: 'FULL-ANSWER-AFTER-RETRY' })).toHaveCount(1)
      await expect(messageContents(page).filter({ hasText: 'partial ' })).toHaveCount(0)
      const retry = messageContents(page).filter({ hasText: 'API retry' })
      await expect(retry).toHaveCount(1)
      await expect(retry).toContainText('succeeded')
    }
  }
  finally {
    const snapshot = await readNativeMessageSnapshot(native, agent.id)
    await testInfo.attach('native-retry-history', {
      contentType: 'application/json',
      body: JSON.stringify(snapshot.messages.map(message => ({
        seq: message.seq.toString(),
        source: message.source,
        frame: nativeMessageBody(message),
      })), null, 2),
    })
  }
})
