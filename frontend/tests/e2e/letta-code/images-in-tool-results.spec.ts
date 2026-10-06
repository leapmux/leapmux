import { watchAgentEvents } from '../helpers/agentEventWatch'
import { readAllAgentMessages } from '../helpers/nativeMessages'
import { selectedAgentTabId } from '../helpers/nativeScenario'
import { lettaViewImageToolCall } from '../helpers/providerToolCalls'
import { expectPngInRequest, PNG_BASE64_PREFIX, runToolImageTurn } from '../helpers/toolImages'
import { chatScrollContainer, toolRows } from '../helpers/ui'
import { expect as lettaExpect, lettaTest } from '../letta-fixtures'
import { nativeContext } from './scenarios'
import { liveToolReturnRow, toolReturnRow } from './toolReturnRows'

lettaTest.describe('Letta Code images in tool results', () => {
  lettaTest('keeps the PNG in model input but receives text-only live and stored rows', async ({ authenticatedVisionLettaWorkspace, page, modelScript, leapmuxServer }, testInfo) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedVisionLettaWorkspace.workspaceId })
    const agentId = await selectedAgentTabId(page)
    const watch = await watchAgentEvents(leapmuxServer, agentId, { label: 'Letta live tool result watch', select: liveToolReturnRow })
    try {
      const { resultRequest } = await runToolImageTurn(context, {
        workingDir: authenticatedVisionLettaWorkspace.workingDir,
        marker: 'letta',
        toolCall: image => lettaViewImageToolCall('letta-view-image', image.path),
      })
      expectPngInRequest(resultRequest)

      const storedRows = (await readAllAgentMessages(context, agentId)).flatMap(message => toolReturnRow(message) ?? [])
      // The read throws the failure of the watch, such as a stream that ended before it.
      const liveRows = watch.items()
      await testInfo.attach('letta-live-tool-results', { body: JSON.stringify(liveRows, null, 2), contentType: 'application/json' })
      await testInfo.attach('letta-stored-tool-results', { body: JSON.stringify(storedRows, null, 2), contentType: 'application/json' })
      lettaExpect(liveRows.length).toBeGreaterThan(0)
      lettaExpect(storedRows.length).toBeGreaterThan(0)
      lettaExpect(liveRows.some(row => row.includes(PNG_BASE64_PREFIX))).toBe(false)
      lettaExpect(storedRows.some(row => row.includes(PNG_BASE64_PREFIX))).toBe(false)
      await lettaExpect(toolRows(page).filter({ hasText: 'ViewImage' }).first()).toBeVisible()
      await lettaExpect(chatScrollContainer(page).locator('button[aria-label="Open image"]')).toHaveCount(0)
    }
    finally {
      watch.cancel()
    }
  })
})
