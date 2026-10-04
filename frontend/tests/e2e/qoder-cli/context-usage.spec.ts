import { ListAgentMessagesRequestSchema, ListAgentMessagesResponseSchema } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { decompressContentToString } from '../../../src/lib/decompress'
import { isObject } from '../../../src/lib/jsonPick'
import { getTestChannel } from '../helpers/api'
import { openAgentInfoCard, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { listAgentsViaAPI } from '../helpers/worktree'
import { expect, qoderTest } from '../qoder-fixtures'

qoderTest.describe('Qoder CLI attachments and context usage', () => {
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
