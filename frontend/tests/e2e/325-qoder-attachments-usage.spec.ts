import { ListAgentMessagesRequestSchema, ListAgentMessagesResponseSchema } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { decompressContentToString } from '../../src/lib/decompress'
import { isObject } from '../../src/lib/jsonPick'
import { getTestChannel } from './helpers/api'
import { exerciseAttachmentDelivery, expectRefusedAttachmentsAbsent } from './helpers/attachmentModelProbe'
import { expectAttachmentOutcome } from './helpers/attachments'
import { openAgentInfoCard, sendMessage, waitForAgentIdle } from './helpers/ui'
import { listAgentsViaAPI } from './helpers/worktree'
import { expect, qoderTest } from './qoder-fixtures'

qoderTest.describe('Qoder CLI attachments and context usage', () => {
  qoderTest('delivers text attachment bytes to the model', async ({ qoderWorkspace, page, modelScript }) => {
    void qoderWorkspace
    await exerciseAttachmentDelivery(page, modelScript, 'text', 'qoder-notes.txt')
  })

  qoderTest('delivers image attachment bytes to the model', async ({ qoderWorkspace, page, modelScript }) => {
    void qoderWorkspace
    await exerciseAttachmentDelivery(page, modelScript, 'image', 'qoder-shot.png')
  })

  qoderTest('refuses a PDF attachment that its native input cannot carry', async ({ qoderWorkspace, page, modelScript }) => {
    void qoderWorkspace
    const rejected = await expectAttachmentOutcome(page, 'pdf', { supported: false })
    await expectRefusedAttachmentsAbsent(page, modelScript, [rejected])
  })

  qoderTest('refuses another binary attachment that its native input cannot carry', async ({ qoderWorkspace, page, modelScript }) => {
    void qoderWorkspace
    const rejected = await expectAttachmentOutcome(page, 'binary', { supported: false })
    await expectRefusedAttachmentsAbsent(page, modelScript, [rejected])
  })

  // The BYOK adapter does not carry the mock's token totals, but its native
  // result states the context-window fill as a ratio.
  qoderTest('the agent info card follows the native context percentage after reload', async ({ qoderWorkspace, page, modelScript, leapmuxServer }) => {
    const usage = { inputTokens: 12000, outputTokens: 40 }
    await modelScript.queue({ text: 'Usage recorded.', usage })
    await sendMessage(page, modelScript.prompt('Reply once.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    const agents = await listAgentsViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, qoderWorkspace.workspaceId)
    expect(agents).toHaveLength(1)
    const channel = await getTestChannel(leapmuxServer.hubUrl, leapmuxServer.adminToken)
    const transcript = await channel.callWorker(leapmuxServer.workerId, 'ListAgentMessages', ListAgentMessagesRequestSchema, ListAgentMessagesResponseSchema, {
      agentId: agents[0]!.id,
      limit: 200,
    })
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
