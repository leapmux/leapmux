import { expect } from '@playwright/test'
import { INTERRUPTION_MARKER, parseAssembledMessage } from '../../../src/components/chat/assembledMessage'
import { mimoResultDivider } from '../../../src/components/chat/providers/mimo/extractors/resultDivider'
import { mimoToolPart } from '../../../src/components/chat/providers/mimo/extractors/toolCommon'
import { MIMO_TOOL_STATUS } from '../../../src/generated/contracts/mimo-protocol'
import { pickString } from '../../../src/lib/jsonPick'
import { withCleanup } from '../helpers/cleanup'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent, nativeAgentById } from '../helpers/nativeScenario'
import { readNativeSidebarSnapshot } from '../helpers/nativeSidebarSnapshot'
import { mimoActorCancelToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { openChildTabFromRow, openHeldChildTab, requireRegistryRow } from '../helpers/subagentRegistry'
import { applyPermissionPreset, assistantBubbles, messageContents, sendMessage, tabById } from '../helpers/ui'
import { expectUnsupportedSubagent } from '../helpers/unsupportedSubagent'
import { mimoTest } from '../mimo-fixtures'
import { MIMO_HELD_CHILD_TURN, mimoChildTurn } from './childScenario'

mimoTest('proves the unsupported native child interrupt route while its original task runs', async ({ native }) => {
  await applyPermissionPreset(native.page, 'bypass')
  await expectUnsupportedSubagent(native, { operation: 'interrupt', openChild: () => openHeldChildTab(native, { childTurn: MIMO_HELD_CHILD_TURN, rootTurnsAfterSpawn: [{ text: 'The actual native child completed.' }] }) })
})

mimoTest('cancels the exact native child while its parent completes normally after reload', async ({ native }) => {
  const { page, modelScript } = native
  await applyPermissionPreset(page, 'bypass')
  const parent = await currentNativeAgent(native)
  const task = 'NATIVECHILDCANCELTASK report one word.'
  const partial = 'The actual child partial answer.'
  const gate = 'mimo-child-cancel-held-answer'
  const parentAnswer = 'The parent finishes the native child cancel normally.'
  await withCleanup(async () => {
    await modelScript.rule({
      name: 'the native child holds its own partial answer',
      when: mimoChildTurn(task),
      respond: { text: `${partial} This suffix never completes.`, stream: { chunkChars: partial.length, delayMs: 0, gates: [{ afterChunk: 1, name: gate }] } },
    })
    const start = await modelScript.queue({
      toolCalls: [spawnSubagentToolCall(native.provider, 'native-child-cancel-spawn', {
        description: 'Native child cancellation',
        prompt: modelScript.prompt(task),
        background: true,
      })],
    }, { text: 'The parent starts its actual background child.' })
    modelScript.allowUnconsumed('The native actor cancellation ends the held child response before its final chunk.')
    await sendMessage(page, modelScript.prompt('Start the scripted native child in the background and answer once.'))
    await modelScript.waitForGate(gate)
    await modelScript.waitForSteps(start + 2)
    await expect(assistantBubbles(page).filter({ hasText: 'The parent starts its actual background child.' })).toBeVisible()
    const spawnRows = (await readNativeMessageSnapshot(native, parent.id)).messages.flatMap((message) => {
      const part = mimoToolPart(nativeMessageBody(message))
      return part?.callId === 'native-child-cancel-spawn' && part.status === MIMO_TOOL_STATUS.Completed ? [{ message, part }] : []
    })
    expect(spawnRows).toHaveLength(1)
    const spawn = spawnRows[0]
    if (!spawn)
      throw new Error('The real native spawn supplied no completed tool part.')
    const actorId = pickString(spawn.part.metadata, 'actorId')
    expect(actorId).toMatch(/\S/)
    expect(spawn.message.spanId).toBe(spawn.part.partId)
    const registry = await readNativeSidebarSnapshot(native, parent.id)
    const taskRow = registry.backgroundTasks.find(task => task.id === spawn.part.partId)
    expect(taskRow?.childAgentId).toMatch(/\S/)
    const row = await requireRegistryRow(page)
    const childId = await openChildTabFromRow(page, row)
    expect(childId).toBe(taskRow?.childAgentId)
    const child = await nativeAgentById(native, childId)
    expect(child?.providerChildKey).toBe(spawn.part.partId)
    expect(child?.spawnSpanId).toBe(spawn.message.spanId)
    await tabById(page, parent.id).click()
    await modelScript.queue({ toolCalls: [mimoActorCancelToolCall('native-child-cancel', actorId)] }, { text: parentAnswer })
    // A native child completion notice can start another parent turn.
    await modelScript.fallback({ text: 'The parent reads its native child cancellation notice.' })
    await sendMessage(page, modelScript.prompt(`Cancel the existing native actor ${actorId} and answer normally.`))
    await expect(assistantBubbles(page).filter({ hasText: parentAnswer })).toBeVisible()
    await expect(row).toHaveAttribute('data-status', 'stopped')
    const proveOwnership = async () => {
      const root = await readNativeMessageSnapshot(native, parent.id)
      expect(root.messages.some(message => parseAssembledMessage(nativeMessageBody(message))?.text === partial)).toBe(false)
      const answer = root.messages.find(message => parseAssembledMessage(nativeMessageBody(message))?.text === parentAnswer)
      expect(answer).toBeDefined()
      expect(answer && parseAssembledMessage(nativeMessageBody(answer))?.completion).toBe('complete')
      const dividers = root.messages.flatMap((message) => {
        const divider = mimoResultDivider(nativeMessageBody(message), message.completion)
        return divider ? [divider] : []
      })
      expect(dividers.length).toBeGreaterThan(0)
      expect(dividers.every(divider => divider.label === 'Turn ended')).toBe(true)
      const own = await readNativeMessageSnapshot(native, childId)
      const childAnswers = own.messages.flatMap((message) => {
        const assembled = parseAssembledMessage(nativeMessageBody(message))
        return assembled?.text === partial ? [assembled] : []
      })
      expect(childAnswers).toEqual([{ kind: 'text', text: partial, completion: 'interrupted' }])
    }
    for (const reload of [false, true]) {
      if (reload)
        await page.reload()
      await tabById(page, parent.id).click()
      await expect(assistantBubbles(page).filter({ hasText: parentAnswer })).toBeVisible()
      await expect(messageContents(page).filter({ hasText: INTERRUPTION_MARKER })).toHaveCount(0)
      await proveOwnership()
      await tabById(page, childId).click()
      await expect(assistantBubbles(page).filter({ hasText: partial })).toBeVisible()
      await expect(messageContents(page).filter({ hasText: INTERRUPTION_MARKER })).toHaveCount(1)
      await expect(assistantBubbles(page).filter({ hasText: parentAnswer })).toHaveCount(0)
    }
  }, async () => {
    await modelScript.releaseGateIfHeld(gate)
  })
})
