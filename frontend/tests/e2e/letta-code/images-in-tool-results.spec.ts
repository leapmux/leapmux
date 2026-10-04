import { join } from 'node:path'
import { create, fromBinary, toBinary } from '@bufbuild/protobuf'
import { ListAgentMessagesRequestSchema, ListAgentMessagesResponseSchema, WatchReplayMode } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { WatchEventsRequestSchema, WatchEventsResponseSchema, WatchMode } from '../../../src/generated/proto/leapmux/v1/workspace_pb'
import { decompressContentToString } from '../../../src/lib/decompress'
import { getTestChannel } from '../helpers/api'
import { lettaViewImageToolCall } from '../helpers/providerToolCalls'
import { toolRows, writeToolImage } from '../helpers/toolImages'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { LETTA_TITLE_RULE, expect as lettaExpect, lettaTest } from '../letta-fixtures'

lettaTest.describe('Letta Code images in tool results', () => {
  lettaTest('keeps the PNG in model input but receives text-only live and stored rows', async ({ authenticatedVisionLettaWorkspace, page, modelScript, leapmuxServer }, testInfo) => {
    const workingDir = authenticatedVisionLettaWorkspace.workingDir
    if (!workingDir)
      throw new Error('the Letta workspace has no working directory')
    const imageName = writeToolImage(workingDir, 'letta')
    const agentId = await page.locator('[data-testid="tab"][data-tab-type="agent"]').first().getAttribute('data-tab-id') ?? ''
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
      await modelScript.rule(LETTA_TITLE_RULE)
      await modelScript.queue(
        { toolCalls: [lettaViewImageToolCall('letta-view-image', join(workingDir, imageName))] },
        { text: 'I inspected the picture.' },
      )
      await sendMessage(page, modelScript.prompt(`Open ${imageName} with ViewImage.`))
      const status = await modelScript.waitForSteps()
      await waitForAgentIdle(page, 180_000)
      lettaExpect(JSON.stringify(status.requests.find(record => record.stepIndex === 1)?.body)).toContain('iVBORw0KGgo')

      const transcript = await channel.callWorker(leapmuxServer.workerId, 'ListAgentMessages', ListAgentMessagesRequestSchema, ListAgentMessagesResponseSchema, { agentId, limit: 200 })
      const storedRows = transcript.messages.map(message => decompressContentToString(message.content, message.contentCompression)).filter((content): content is string => !!content && content.includes('tool_return_message'))
      await testInfo.attach('letta-live-tool-results', { body: JSON.stringify(liveRows, null, 2), contentType: 'application/json' })
      await testInfo.attach('letta-stored-tool-results', { body: JSON.stringify(storedRows, null, 2), contentType: 'application/json' })
      lettaExpect(liveRows.length).toBeGreaterThan(0)
      lettaExpect(storedRows.length).toBeGreaterThan(0)
      lettaExpect(liveRows.some(row => row.includes('iVBORw0KGgo'))).toBe(false)
      lettaExpect(storedRows.some(row => row.includes('iVBORw0KGgo'))).toBe(false)
      await lettaExpect(toolRows(page).filter({ hasText: 'ViewImage' }).first()).toBeVisible()
      await lettaExpect(page.locator('[data-chat-scroll-container="true"]:visible button[aria-label="Open image"]')).toHaveCount(0)
    }
    finally {
      watch.cancel()
    }
  })
})
