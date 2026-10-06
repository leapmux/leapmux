import { expect } from '@playwright/test'
import { decompressContentToString } from '../../../src/lib/decompress'
import { isObject } from '../../../src/lib/jsonPick'
import { SCRIPTED_CONTEXT_USAGE } from '../helpers/contextUsage'
import { readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { selectedAgentTabId } from '../helpers/nativeScenario'
import { openAgentInfoCard, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { qoderTest } from '../qoder-fixtures'

qoderTest.describe('Qoder CLI attachments and context usage', () => {
  qoderTest('the agent info card follows the native context percentage after reload', async ({ native }) => {
    const { page, modelScript } = native
    const step = await modelScript.queue({ text: 'Usage recorded.', usage: { ...SCRIPTED_CONTEXT_USAGE } })
    await sendMessage(page, modelScript.prompt('Reply once.'))
    await modelScript.waitForSteps(step + 1)
    await waitForAgentIdle(page)

    const transcript = await readNativeMessageSnapshot(native, await selectedAgentTabId(page))
    const ratios = transcript.messages.flatMap((row) => {
      const raw = decompressContentToString(row.content, row.contentCompression)
      if (!raw?.startsWith('{"type":"result"'))
        return []
      const frame: unknown = JSON.parse(raw)
      if (!isObject(frame) || !isObject(frame.usage))
        return []
      const ratio = frame.usage.context_usage_ratio
      return typeof ratio === 'number' && Number.isFinite(ratio) && ratio >= 0 ? [ratio] : []
    })
    const ratio = ratios.at(-1)
    if (ratio === undefined)
      throw new Error('Qoder returned no native context ratio')
    expect(ratio).toBeGreaterThan(0)
    const label = `${Math.round(Math.min(ratio, 1) * 100)}% of the context window`
    await expect(await openAgentInfoCard(page)).toContainText(label)
    await page.reload()
    await expect(await openAgentInfoCard(page)).toContainText(label)
  })
})
