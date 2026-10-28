import { randomUUID } from 'node:crypto'
import { expect } from '@playwright/test'
import { museNativeParentInput } from '../../../src/components/chat/providers/muse/delivery'
import { museItem } from '../../../src/components/chat/providers/muse/protocol'
import { museNativeResult } from '../../../src/components/chat/providers/muse/sourceData'
import { MUSE_PARENT_INPUT } from '../../../src/generated/contracts/muse-protocol'
import { AgentInputQueuePauseReason, AgentInputState } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { pickObject, pickString } from '../../../src/lib/jsonPick'
import { parseMessageContent } from '../../../src/lib/messageParser'
import { startModelScript } from '../helpers/modelScriptFixture'
import { readNativeInputQueue } from '../helpers/nativeInputQueueIdle'
import { expectNativeInputRefusal } from '../helpers/nativeInputQueueRefusal'
import { nativeMessageBody, nativeMessagesHoldingText, nativeMessageSupplement, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { nativeModelContextText } from '../helpers/nativeScenario'
import { exerciseWorkerRestart } from '../helpers/nativeWorkerRestart'
import { bashToolCall, museSendChildMessageToolCall, museSpawnChildToolCall, museSubmitResultToolCall } from '../helpers/providerToolCalls'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { backgroundTaskRows, expandBackgroundTasksSection, openChildTabFromRow } from '../helpers/subagentRegistry'
import { assistantBubbles, chatScrollContainer, sendMessage, tabById, userBubbles, waitForAgentIdle } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { MUSE_E2E_SKIP_REASON, museTest } from '../muse-fixtures'
import { createProviderProcessTest } from '../provider-fixture-factory'
import { MUSE_AGENT, nativeContext } from './scenarios'
import { expectMuseChildResult, MUSE_CHILD_OUTPUT, MUSE_CHILD_PROGRESS, MUSE_CHILD_TASK, openMuseChild, readMuseNativeControlRecords } from './subagent-common.muse'

museTest('opens the actual child history and preserves its native session after reload', async ({ native }, testInfo) => {
  const { page } = native
  const child = await openMuseChild(native, false)
  try {
    for (const reload of [false, true]) {
      if (reload)
        await page.reload()
      await tabById(page, child.childId).click()
      await waitForAgentIdle(page)
      await expect(userBubbles(page).filter({ hasText: MUSE_CHILD_TASK })).toHaveCount(1)
      await expect(chatScrollContainer(page).getByText(MUSE_CHILD_PROGRESS, { exact: true })).toBeVisible()
      await expect(chatScrollContainer(page).getByText(MUSE_CHILD_OUTPUT, { exact: true })).toBeVisible()
      await expectMuseChildResult(native)
      const snapshot = await readNativeMessageSnapshot(native, child.childId)
      expect(snapshot.agentSessionId).not.toBe('')
      const frames = snapshot.messages.map(message => nativeMessageBody(message))
      expect(JSON.stringify(frames)).toContain(MUSE_CHILD_OUTPUT)
      const spans = snapshot.messages.filter(message => message.spanId)
      expect(spans.length).toBeGreaterThan(0)
      expect(spans.every(message => message.agentSessionId === snapshot.agentSessionId)).toBe(true)
    }
  }
  finally {
    for (const agentId of [child.parentId, child.childId]) {
      const snapshot = await readNativeMessageSnapshot(native, agentId)
      await testInfo.attach(`muse-native-transcript-${agentId}`, {
        body: JSON.stringify({ agentId, agentSessionId: snapshot.agentSessionId, messages: snapshot.messages.map(message => ({ seq: String(message.seq), sessionId: message.agentSessionId, source: message.source, original: nativeMessageBody(message), supplement: nativeMessageSupplement(message) })) }),
        contentType: 'application/json',
      })
    }
  }
})

const restartTest = createProviderProcessTest({
  agent: MUSE_AGENT,
  nativeContext,
  skipReason: MUSE_E2E_SKIP_REASON,
  openOptions: { optionValues: { workspaceTrust: 'agent', permissionMode: 'allowAll' } },
})

restartTest('restores the same native child route and history after its owner restarts', async ({ native, separateHubWorker }) => {
  const { page, modelScript } = native
  const child = await openMuseChild(native, false)
  const before = await readNativeMessageSnapshot(native, child.childId)
  await tabById(page, child.parentId).click()
  await exerciseWorkerRestart(native, separateHubWorker, 'worker')
  await tabById(page, child.childId).click()
  await expectMuseChildResult(native)
  const after = await readNativeMessageSnapshot(native, child.childId)
  expect(after.agentSessionId).toBe(before.agentSessionId)
  expect(after.messages.map(message => message.id)).toEqual(before.messages.map(message => message.id))
  const note = 'MUSERESTOREDCHILDNOTE: read this after your owner restarts.'
  const answer = 'The restored child read the exact new note.'
  const rule = 'the restored Muse child reads its exact note'
  await modelScript.rule({
    name: rule,
    when: { body: ['Workflow child completion protocol', note] },
    once: true,
    respond: { text: answer, toolCalls: [museSubmitResultToolCall('muse-restored-child-submit', 'The restored child processed its note.')] },
  })
  await sendMessage(page, note)
  await expect.poll(async () => (await modelScript.status()).ruleMatches[rule] ?? 0).toBe(1)
  await expect(assistantBubbles(page).filter({ hasText: answer })).toHaveCount(1)
  await waitForAgentIdle(page)
  await page.reload()
  await tabById(page, child.childId).click()
  await expect(userBubbles(page).filter({ hasText: MUSE_CHILD_TASK })).toHaveCount(1)
  await expect(userBubbles(page).filter({ hasText: note })).toHaveCount(1)
  await expect(assistantBubbles(page).filter({ hasText: answer })).toHaveCount(1)
  await tabById(page, child.parentId).click()
  await expect(userBubbles(page).filter({ hasText: note })).toHaveCount(0)
})

// Track the native route and its LeapMux integration: https://github.com/leapmux/leapmux/issues/516.

for (const active of [true, false]) {
  museTest(`preserves the parent instruction before the ${active ? 'running' : 'completed'} child answer`, async ({ native }, testInfo) => {
    const { page, modelScript } = native
    const mockUrl = native.leapmuxServer.mockModelUrl
    if (!mockUrl)
      throw new Error('The Muse parent-input scenario requires the isolated mock endpoint.')
    const childLifecycle = await startModelScript(mockUrl, { testDeadline: modelScript.testDeadline })
    const childScript = childLifecycle.script
    const task = `MUSE_PARENT_INPUT_TASK_${active ? 'ACTIVE' : 'COMPLETED'}`
    const note = `MUSE_PARENT_INSTRUCTION_${active ? 'ACTIVE' : 'COMPLETED'}`
    const answer = 'The child processed its native parent instruction.'
    const humanAnswer = 'The child processed the later human input.'
    const gate = 'muse-native-parent-input'
    const spawnCallId = 'muse-native-parent-spawn'
    const initialAnswer = 'The initial native child task completed.'
    const notices = [answer, humanAnswer, ...active ? [] : [initialAnswer]]
    await modelScript.rule(...notices.map(summary => ({
      name: `the parent receives ${summary}`,
      when: { body: ['<runtime-context source=', 'subagent_result', summary] },
      once: true,
      respond: { text: `The parent acknowledged: ${summary}` },
    })))
    const waitForNotice = async (summary: string) => {
      await expect.poll(async () => (await modelScript.status()).ruleMatches[`the parent receives ${summary}`] ?? 0).toBe(1)
    }
    let complete = false
    let parentSnapshot: Awaited<ReturnType<typeof readNativeMessageSnapshot>> | undefined
    let diagnosticChildId: string | undefined
    let diagnosticParentId: string | undefined
    try {
      const { agentId: parentId } = await openProviderAgent(native.leapmuxServer, native.workspaceId, MUSE_AGENT, {
        optionValues: { workspaceTrust: 'agent', permissionMode: 'allowAll' },
      })
      diagnosticParentId = parentId
      await tabById(page, parentId).click()
      await childScript.queue(
        active
          ? { gate, toolCalls: [bashToolCall(native.provider, 'muse-parent-input-boundary', 'printf \'MUSE_PARENT_INPUT_BOUNDARY\\n\'')] }
          : { text: initialAnswer },
        { text: answer },
      )
      const start = await modelScript.queue(
        { toolCalls: [museSpawnChildToolCall(spawnCallId, randomUUID(), childScript.prompt(task))] },
        { text: 'The parent spawned its native child.' },
      )
      await sendMessage(page, modelScript.prompt('Spawn the native child for the transcript test.'))
      await modelScript.waitForSteps(start + 2)
      await expect(assistantBubbles(page).filter({ hasText: 'The parent spawned its native child.' })).toHaveCount(1)
      const subagentId = await retryUntilPass(async () => {
        const snapshot = await readNativeMessageSnapshot(native, parentId)
        parentSnapshot = snapshot
        const results = snapshot.messages.flatMap((message) => {
          const frame = nativeMessageBody(message)
          if (museItem(frame)?.callId !== spawnCallId)
            return []
          const result = museNativeResult(frame, parseMessageContent(message).supplementalContent)
          return result?.structured ? [result.structured] : []
        })
        expect(results).toHaveLength(1)
        expect(results[0]?.status).toBe('accepted')
        const id = pickString(results[0], 'subagent_id')
        expect(id).not.toBe('')
        return id
      })
      await expandBackgroundTasksSection(page)
      const row = backgroundTaskRows(page, { kind: 'subagent' }).filter({ hasText: task }).first()
      if (active) {
        await childScript.waitForGate(gate)
      }
      else {
        await expect(row).toHaveAttribute('data-status', 'succeeded')
        await waitForNotice(initialAnswer)
      }
      const childId = await openChildTabFromRow(page, row)
      diagnosticChildId = childId
      await tabById(page, parentId).click()
      const send = await modelScript.queue(
        { toolCalls: [museSendChildMessageToolCall('muse-native-parent-send', randomUUID(), subagentId, note, active ? 'queue' : 'followup')] },
        { text: 'The parent delivered its native instruction.' },
      )
      // Only the native parent tool supplies the note to the child.
      await sendMessage(page, modelScript.prompt('Deliver the scripted parent instruction.'))
      await modelScript.waitForSteps(send + 2)
      await expect(assistantBubbles(page).filter({ hasText: 'The parent delivered its native instruction.' })).toHaveCount(1)
      if (active)
        await childScript.releaseGate(gate)
      await childScript.waitForSteps(2)
      expect(nativeModelContextText(await childScript.requestAt(1))).toContain(note)
      await tabById(page, childId).click()
      await expect(assistantBubbles(page).filter({ hasText: answer })).toHaveCount(1)
      await waitForAgentIdle(page)
      await waitForNotice(answer)
      const beforeReload = await readNativeMessageSnapshot(native, childId)
      const parentInputs = beforeReload.messages.filter(message => museNativeParentInput(nativeMessageBody(message), parseMessageContent(message).supplementalContent, beforeReload.agentSessionId) === note)
      expect(parentInputs).toHaveLength(1)
      const replies = nativeMessagesHoldingText(beforeReload.messages, answer)
      expect(replies).toHaveLength(1)
      expect(parentInputs[0]!.seq).toBeLessThan(replies[0]!.seq)
      expect(parentInputs[0]!.agentSessionId).toBe(beforeReload.agentSessionId)
      await expect(userBubbles(page).filter({ hasText: note })).toHaveCount(1)
      await page.reload()
      await tabById(page, childId).click()
      await expect(userBubbles(page).filter({ hasText: note })).toHaveCount(1)
      await expect(assistantBubbles(page).filter({ hasText: answer })).toHaveCount(1)
      const afterReload = await readNativeMessageSnapshot(native, childId)
      expect(afterReload.messages.map(message => message.id)).toEqual(beforeReload.messages.map(message => message.id))
      // Equal bodies remain separate inputs when different actors sent them.
      const humanRule = 'the native child reads its separate human input'
      await childScript.rule({ name: humanRule, when: { body: note }, once: true, respond: { text: humanAnswer } })
      await sendMessage(page, note)
      const delivered = await retryUntilPass(async () => {
        const queue = await readNativeInputQueue(native.leapmuxServer, childId)
        const failed = queue.items.find(item => item.state === AgentInputState.FAILED)
        if (failed) {
          expect(active).toBe(false)
          expect(queue.paused).toBe(true)
          expect(queue.pauseReason).toBe(AgentInputQueuePauseReason.DELIVERY_FAILED)
          expect(failed.text).toBe(note)
          expect(failed.error).toContain('subagent/followupTask')
          expect(failed.error).toContain('subagent_state_ineligible')
          return false
        }
        expect((await childScript.status()).ruleMatches[humanRule] ?? 0).toBe(1)
        return true
      })
      if (!delivered) {
        // Muse 1.4.13 can refuse public child input after a model-driven resume.
        // Consuming the ready result does not change that native refusal.
        const failed = await expectNativeInputRefusal(native, 'subagent_state_ineligible')
        expect(failed.text).toBe(note)
        const parentSessionId = parentSnapshot?.agentSessionId
        if (!parentSessionId)
          throw new Error('The native refusal requires its exact owning parent session.')
        const records = await readMuseNativeControlRecords(parentSessionId, native.leapmuxServer.agentEnv?.XDG_DATA_HOME)
        const received = records.map(record => pickObject(record, 'payload')).filter(payload => payload?.kind === MUSE_PARENT_INPUT.CommandIntake).map(payload => pickObject(payload, 'record')).filter(record => record?.kind === 'received' && pickObject(record, 'command')?.verb === 'followup_task' && pickObject(record, 'command')?.body === note).at(-1)
        expect(pickObject(received, 'command')?.subagent_id).toBe(subagentId)
        expect(pickObject(received, 'session_stream')?.id).toBe(parentSessionId)
        const commandId = pickString(received, 'command_id')
        expect(commandId).not.toBe('')
        const settled = records.map(record => pickObject(record, 'payload')).filter(payload => payload?.kind === MUSE_PARENT_INPUT.CommandIntake).map(payload => pickObject(payload, 'record')).find(record => record?.kind === 'settled' && record.command_id === commandId)
        expect(pickObject(settled, 'outcome')).toMatchObject({ kind: 'rejected', reason: 'subagent_state_ineligible' })
        await expect(userBubbles(page).filter({ hasText: note })).toHaveCount(1)
        expect((await childScript.status()).ruleMatches[humanRule] ?? 0).toBe(0)
        await page.reload()
        await tabById(page, childId).click()
        const retained = await expectNativeInputRefusal(native, 'subagent_state_ineligible')
        expect(retained.id).toBe(failed.id)
        expect(retained.text).toBe(note)
        const unchanged = await readNativeMessageSnapshot(native, childId)
        expect(unchanged.messages.map(message => message.id)).toEqual(afterReload.messages.map(message => message.id))
        await expect(userBubbles(page).filter({ hasText: note })).toHaveCount(1)
        await expect(assistantBubbles(page).filter({ hasText: answer })).toHaveCount(1)
        complete = true
        return
      }
      await expect.poll(async () => (await childScript.status()).ruleMatches[humanRule] ?? 0).toBe(1)
      await expect(assistantBubbles(page).filter({ hasText: humanAnswer })).toHaveCount(1)
      await waitForAgentIdle(page)
      await expect(userBubbles(page).filter({ hasText: note })).toHaveCount(2)
      await waitForNotice(humanAnswer)
      await page.reload()
      await tabById(page, childId).click()
      await expect(userBubbles(page).filter({ hasText: note })).toHaveCount(2)
      await expect(assistantBubbles(page).filter({ hasText: answer })).toHaveCount(1)
      await expect(assistantBubbles(page).filter({ hasText: humanAnswer })).toHaveCount(1)
      const final = await readNativeMessageSnapshot(native, childId)
      expect(final.messages.filter(message => museNativeParentInput(nativeMessageBody(message), parseMessageContent(message).supplementalContent, final.agentSessionId) === note)).toHaveLength(1)
      expect(nativeMessagesHoldingText(final.messages, note)).toHaveLength(2)
      await tabById(page, parentId).click()
      await expect(userBubbles(page).filter({ hasText: note })).toHaveCount(0)
      complete = true
    }
    finally {
      await childScript.releaseGateIfHeld(gate)
      if (!complete && parentSnapshot) {
        try {
          const records = await readMuseNativeControlRecords(parentSnapshot.agentSessionId, native.leapmuxServer.agentEnv?.XDG_DATA_HOME)
          await testInfo.attach('muse-native-parent-control-records', { body: JSON.stringify(records), contentType: 'application/json' })
        }
        catch (error) {
          await testInfo.attach('muse-native-control-diagnostic-error', { body: String(error), contentType: 'text/plain' })
        }
      }
      for (const agentId of [diagnosticParentId, diagnosticChildId]) {
        if (!agentId)
          continue
        try {
          const snapshot = await readNativeMessageSnapshot(native, agentId)
          const queue = await readNativeInputQueue(native.leapmuxServer, agentId)
          await testInfo.attach(`muse-native-delivery-state-${agentId}`, {
            body: JSON.stringify({ sessionId: snapshot.agentSessionId, queue, messages: snapshot.messages.map(message => ({ seq: String(message.seq), source: message.source, original: nativeMessageBody(message), supplement: nativeMessageSupplement(message) })) }, (_key, value) => typeof value === 'bigint' ? String(value) : value),
            contentType: 'application/json',
          })
        }
        catch (error) {
          await testInfo.attach('muse-native-delivery-diagnostic-error', { body: String(error), contentType: 'text/plain' })
        }
      }
      if (parentSnapshot) {
        await testInfo.attach('muse-native-parent-tool-transcript', {
          body: JSON.stringify({ sessionId: parentSnapshot.agentSessionId, messages: parentSnapshot.messages.map(message => ({ seq: String(message.seq), source: message.source, spanId: message.spanId, original: nativeMessageBody(message), supplement: nativeMessageSupplement(message) })) }),
          contentType: 'application/json',
        })
      }
      await testInfo.attach('muse-native-parent-input-model', { body: JSON.stringify(await childScript.status()), contentType: 'application/json' })
      await childLifecycle.finish(complete)
    }
  })
}
