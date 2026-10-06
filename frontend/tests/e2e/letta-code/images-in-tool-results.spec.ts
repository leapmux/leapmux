import { create, fromBinary, toBinary } from '@bufbuild/protobuf'
import { WatchReplayMode } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { WatchEventsRequestSchema, WatchEventsResponseSchema, WatchMode } from '../../../src/generated/proto/leapmux/v1/workspace_pb'
import { decompressContentToString } from '../../../src/lib/decompress'
import { getTestChannel } from '../helpers/api'
import { readAllAgentMessages } from '../helpers/nativeMessages'
import { selectedAgentTabId } from '../helpers/nativeScenario'
import { lettaViewImageToolCall } from '../helpers/providerToolCalls'
import { expectPngInRequest, PNG_BASE64_PREFIX, runToolImageTurn } from '../helpers/toolImages'
import { chatScrollContainer, toolRows } from '../helpers/ui'
import { expect as lettaExpect, lettaTest } from '../letta-fixtures'
import { nativeContext } from './scenarios'

lettaTest.describe('Letta Code images in tool results', () => {
  lettaTest('keeps the PNG in model input but receives text-only live and stored rows', async ({ authenticatedVisionLettaWorkspace, page, modelScript, leapmuxServer }, testInfo) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedVisionLettaWorkspace.workspaceId })
    const agentId = await selectedAgentTabId(page)
    const channel = await getTestChannel(leapmuxServer.hubUrl, leapmuxServer.adminToken)
    const channelId = await channel.getOrOpenChannel(leapmuxServer.workerId)
    const request = create(WatchEventsRequestSchema, {
      agents: [{ agentId, mode: WatchMode.FULL, replay: WatchReplayMode.LATEST, cursorSeq: 0n }],
      updateId: 1n,
    })
    const watch = channel.stream(channelId, 'WatchEvents', toBinary(WatchEventsRequestSchema, request))
    const liveRows: string[] = []
    let subscribed = false
    watch.onMessage((frame) => {
      const response = fromBinary(WatchEventsResponseSchema, frame.payload)
      if (response.event.case === 'updateAck') {
        subscribed = response.event.value.updateId === 1n && response.event.value.rejectedAgents.length === 0
        return
      }
      if (response.event.case !== 'agentEvent')
        return
      const event = response.event.value
      if (event.agentId !== agentId || event.replay || event.event.case !== 'agentMessage')
        return
      const message = event.event.value
      const raw = decompressContentToString(message.content, message.contentCompression)
      if (raw?.includes('tool_return_message'))
        liveRows.push(raw)
    })
    try {
      await lettaExpect.poll(() => subscribed).toBe(true)
      const { resultRequest } = await runToolImageTurn(context, {
        workingDir: authenticatedVisionLettaWorkspace.workingDir,
        marker: 'letta',
        toolCall: image => lettaViewImageToolCall('letta-view-image', image.path),
      })
      expectPngInRequest(resultRequest)

      const storedRows = (await readAllAgentMessages(context, agentId))
        .map(message => decompressContentToString(message.content, message.contentCompression))
        .filter((content): content is string => !!content && content.includes('tool_return_message'))
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
