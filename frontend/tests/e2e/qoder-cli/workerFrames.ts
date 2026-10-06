import type { TestInfo } from '@playwright/test'
import { writeFileSync } from 'node:fs'
import { ListAgentMessagesRequestSchema, ListAgentMessagesResponseSchema } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { decompressContentToString } from '../../../src/lib/decompress'
import { getTestChannel } from '../helpers/api'

interface QoderFrameServer {
  hubUrl: string
  adminToken: string
  workerId: string
}

/** Attach bounded native frames from one Qoder root or child transcript. */
export async function attachQoderWorkerFrames(
  testInfo: TestInfo,
  server: QoderFrameServer,
  agentId: string,
  transcript: 'root' | 'child' = 'root',
): Promise<void> {
  const channel = await getTestChannel(server.hubUrl, server.adminToken)
  const response = await channel.callWorker(
    server.workerId,
    'ListAgentMessages',
    ListAgentMessagesRequestSchema,
    ListAgentMessagesResponseSchema,
    { agentId, limit: 200 },
  )
  const markers = ['spawn-qoder', 'parent_tool_use_id', '"type":"result"', 'task_started', 'task_notification', 'Workflow']
  const rows = (response.messages ?? []).flatMap((message) => {
    const raw = decompressContentToString(message.content, message.contentCompression)
    if (!raw)
      return []
    const first = transcript === 'child'
      ? 0
      : Math.min(...markers.map(marker => raw.indexOf(marker)).filter(index => index >= 0))
    if (!Number.isFinite(first))
      return []
    return [{ seq: message.seq.toString(), excerpt: raw.slice(Math.max(0, first - 250), first + 4500) }]
  }).slice(0, 60)
  const name = `qoder-${transcript}-worker-frames`
  const path = testInfo.outputPath(`${name}.json`)
  writeFileSync(path, JSON.stringify(rows))
  await testInfo.attach(name, { path, contentType: 'application/json' })
}
