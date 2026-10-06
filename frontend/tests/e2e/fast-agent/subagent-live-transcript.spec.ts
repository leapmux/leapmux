import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { ListAgentMessagesRequestSchema, ListAgentMessagesResponseSchema } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { decompressContentToString } from '../../../src/lib/decompress'
import { fastAgentTest } from '../fastagent-fixtures'
import { getTestChannel } from '../helpers/api'
import { withCleanup } from '../helpers/cleanup'
import { currentNativeAgent, nativeTextStep } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { readToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { uniqueMarker } from '../helpers/shellArguments'
import { expectRowBecomesFinal, openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { messageContents, sendMessage, tabById, waitForAgentIdle } from '../helpers/ui'
import { nativeContext } from './scenarios'

fastAgentTest('keeps the actual child read out of live rows and restores it from the final archive', async ({ authenticatedFastAgentWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedFastAgentWorkspace.workspaceId })
  const parent = await currentNativeAgent(context)
  const suffix = uniqueMarker()
  const task = `FASTLIVEREADTASK${suffix} read the supplied file.`
  const marker = `FASTLIVEREADVALUE${suffix}`
  const path = join(parent.workingDir, 'native-child-read.txt')
  const firstGate = `fast-read-first-${suffix}`
  const finalGate = `fast-read-final-${suffix}`
  writeFileSync(path, marker)
  const start = (await modelScript.status()).stepCount
  await modelScript.rule(
    { name: 'fast-native-child-read', when: { body: task }, respond: { gate: firstGate, toolCalls: [readToolCall(context.provider, 'native-live-read', path)] }, once: true },
    { name: 'fast-native-child-final', when: { body: task }, respond: { gate: finalGate, text: 'The native child read completed.' }, once: true },
  )
  await modelScript.queue(
    { toolCalls: [spawnSubagentToolCall(context.provider, 'fast-live-spawn', { description: 'Native held Read', prompt: modelScript.prompt(task) })] },
    nativeTextStep(context, 'The native parent completed.'),
  )
  await withCleanup(async () => {
    await sendMessage(page, modelScript.prompt('Start the native child Read and report its result.'))
    await modelScript.waitForGate(firstGate)
    const row = await requireRegistryRow(page)
    const childId = await openChildTabFromRow(page, row)
    await modelScript.releaseGate(firstGate)
    const allow = page.locator('[data-testid="control-allow-btn"]:visible')
    await expect.poll(async () => (await modelScript.status()).pendingGates.includes(finalGate) ? 'held' : await allow.isVisible() ? 'approval' : 'waiting').not.toBe('waiting')
    if (!(await modelScript.status()).pendingGates.includes(finalGate)) {
      await expect(page.locator('[data-testid="control-banner"]:visible')).toContainText('read_text_file')
      await allow.click()
    }
    const held = await modelScript.waitForGate(finalGate)
    expect(nativeToolResult(held.requests.find(request => request.rule === 'fast-native-child-final'), 'native-live-read')).toContain(marker)
    await expect(row).toHaveAttribute('data-status', 'running')
    const channel = await getTestChannel(leapmuxServer.hubUrl, leapmuxServer.adminToken)
    const messages = await channel.callWorker(leapmuxServer.workerId, 'ListAgentMessages', ListAgentMessagesRequestSchema, ListAgentMessagesResponseSchema, { agentId: childId, limit: 200 })
    expect(messages.messages.some(message => decompressContentToString(message.content, message.contentCompression)?.includes(marker))).toBe(false)
    await expect(messageContents(page).filter({ hasText: marker })).toHaveCount(0)
    await modelScript.releaseGate(finalGate)
    await tabById(page, parent.id).click()
    await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)
    await expectRowBecomesFinal(page, row)
    await tabById(page, childId).click()
    await expect(messageContents(page).filter({ hasText: marker }).first()).toBeVisible()
    await page.reload()
    await expect(messageContents(page).filter({ hasText: marker }).first()).toBeVisible()
  }, async () => {
    for (const gate of [firstGate, finalGate]) {
      if ((await modelScript.status()).pendingGates.includes(gate))
        await modelScript.releaseGate(gate)
    }
  })
})
