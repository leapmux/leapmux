import { expect } from '@playwright/test'
import { fastAgentTest } from '../fastagent-fixtures'
import { finishCleanup, withCleanup } from '../helpers/cleanup'
import { expectStoredMessagesLack, writeChildMarkerFile } from '../helpers/liveChildTranscript'
import { ruleRequest } from '../helpers/mockModelScript'
import { currentNativeAgent, nativeTextStep } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { readToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { uniqueMarker } from '../helpers/shellArguments'
import { expectRowBecomesFinal, openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { messageContents, sendMessage, tabById, waitForAgentIdle } from '../helpers/ui'
import { allowReadIfAsked } from './readPermission'

// Fast Agent restores the transcript of a child from its final archive, so the Read result shows after the
// completion. The Read can ask for an approval, so the script holds the child before and after the Read.
fastAgentTest('keeps the actual child read out of live rows and restores it from the final archive', async ({ native }) => {
  const { page, modelScript } = native
  const parent = await currentNativeAgent(native)
  const suffix = uniqueMarker()
  const task = `FASTLIVEREADTASK${suffix} read the supplied file.`
  const file = writeChildMarkerFile(parent.workingDir, 'native-child-read.txt')
  const firstGate = `fast-read-first-${suffix}`
  const finalGate = `fast-read-final-${suffix}`
  await modelScript.rule(
    { name: 'fast-native-child-read', when: { body: task }, respond: { gate: firstGate, toolCalls: [readToolCall(native.provider, 'native-live-read', file.path)] }, once: true },
    { name: 'fast-native-child-final', when: { body: task }, respond: { gate: finalGate, text: 'The native child read completed.' }, once: true },
  )
  const start = await modelScript.queue(
    { toolCalls: [spawnSubagentToolCall(native.provider, 'fast-live-spawn', { description: 'Native held Read', prompt: modelScript.prompt(task) })] },
    nativeTextStep(native, 'The native parent completed.'),
  )
  await withCleanup(async () => {
    await sendMessage(page, modelScript.prompt('Start the native child Read and report its result.'))
    await modelScript.waitForGate(firstGate)
    const row = await requireRegistryRow(page)
    const childId = await openChildTabFromRow(page, row)
    await modelScript.releaseGate(firstGate)
    await allowReadIfAsked(page, async () => (await modelScript.status()).pendingGates.includes(finalGate))
    const held = await modelScript.waitForGate(finalGate)
    expect(nativeToolResult(ruleRequest(held, 'fast-native-child-final'), 'native-live-read')).toContain(file.marker)
    await expect(row).toHaveAttribute('data-status', 'running')
    await expectStoredMessagesLack(native, childId, file.marker)
    await expect(messageContents(page).filter({ hasText: file.marker })).toHaveCount(0)
    await modelScript.releaseGate(finalGate)
    await tabById(page, parent.id).click()
    await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)
    await expectRowBecomesFinal(page, row)
    await tabById(page, childId).click()
    await expect(messageContents(page).filter({ hasText: file.marker }).first()).toBeVisible()
    await page.reload()
    await expect(messageContents(page).filter({ hasText: file.marker }).first()).toBeVisible()
  }, () => finishCleanup([modelScript.releaseGateIfHeld(firstGate), modelScript.releaseGateIfHeld(finalGate)]))
})
