import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { ListAgentMessagesRequestSchema, ListAgentMessagesResponseSchema } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { decompressContentToString } from '../../../src/lib/decompress'
import { diracTest } from '../dirac-fixtures'
import { getTestChannel } from '../helpers/api'
import { withCleanup } from '../helpers/cleanup'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { readToolCall } from '../helpers/providerToolCalls'
import { uniqueMarker } from '../helpers/shellArguments'
import { openChildTabFromRow } from '../helpers/subagentRegistry'
import { messageContents, tabById } from '../helpers/ui'
import { nativeContext, runningChild } from './scenarios'

diracTest('keeps an actual child read out of live rows and restores it after the final native report', async ({ authenticatedDiracWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDiracWorkspace.workspaceId })
  const parent = await currentNativeAgent(context)
  const marker = uniqueMarker('NATIVECHILDREAD')
  const path = join(parent.workingDir, 'native-child-read.txt')
  writeFileSync(path, marker)
  const child = await runningChild(context, { childTool: readToolCall(context.provider, 'native-live-read', path) })
  await withCleanup(async () => {
    const status = await modelScript.status()
    const nativeRead = status.requests.find(request => JSON.stringify(request.body).includes(marker))
    expect(nativeToolResult(nativeRead, 'native-live-read')).toContain(marker)
    await expect(child.row).toHaveAttribute('data-status', 'running')
    await openChildTabFromRow(page, child.row)
    const channel = await getTestChannel(leapmuxServer.hubUrl, leapmuxServer.adminToken)
    const live = await channel.callWorker(leapmuxServer.workerId, 'ListAgentMessages', ListAgentMessagesRequestSchema, ListAgentMessagesResponseSchema, { agentId: child.childId, limit: 200 })
    expect(live.messages.some(message => decompressContentToString(message.content, message.contentCompression)?.includes(marker))).toBe(false)
    await expect(messageContents(page).filter({ hasText: marker })).toHaveCount(0)
  }, child.finish)
  await tabById(page, child.childId).click()
  await expect(messageContents(page).filter({ hasText: marker }).first()).toBeVisible()
  await page.reload()
  await expect(messageContents(page).filter({ hasText: marker }).first()).toBeVisible()
})
