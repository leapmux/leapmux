import { expect } from '@playwright/test'
import { isObject, pickObject, pickString } from '../../../src/lib/jsonPick'
import { antigravityTest } from '../antigravity-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { ruleRequest } from '../helpers/mockModelScript'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent, nativeModelContextText } from '../helpers/nativeScenario'
import { antigravitySendMessageToolCall } from '../helpers/providerToolCalls'
import { uniqueMarker } from '../helpers/shellArguments'
import { AGENT_TAB_SELECTOR, tabIdSelector } from '../helpers/tabSelectors'
import { messageContents, sendMessage, userBubbles, waitForAgentIdle } from '../helpers/ui'
import { finishAntigravityChildWithReload, openAntigravityRunningChild } from './childScenarios'

antigravityTest('sends a new prompt to the finished native child and revives its row', async ({ native }) => {
  const child = await openAntigravityRunningChild(native)
  await finishAntigravityChildWithReload(native, child)
  const task = 'Answer the follow-up in the same native child.'
  const prompt = native.modelScript.prompt(task)
  const answer = 'ANTIGRAVITY_CHILD_FOLLOWUP_ANSWER'
  const gate = 'antigravity-child-followup'
  await native.modelScript.rule({
    name: 'the native child answers its follow-up',
    when: { user: task },
    respond: { text: answer, gate },
    once: true,
  })
  await withCleanup(async () => {
    await sendMessage(native.page, prompt)
    await native.modelScript.waitForGate(gate)
    await expect(child.row).toHaveAttribute('data-status', 'running')
    await native.modelScript.releaseGate(gate)
    await waitForAgentIdle(native.page)
    await expect(child.row).toHaveAttribute('data-status', 'succeeded')
    await expect(messageContents(native.page).filter({ hasText: answer }).first()).toBeVisible()
    for (const reload of [false, true]) {
      if (reload)
        await native.page.reload()
      await expect(userBubbles(native.page).filter({ hasText: task })).toHaveCount(1)
      const snapshot = await readNativeMessageSnapshot(native, child.childId)
      const frames = snapshot.messages.map(nativeMessageBody).filter(isObject)
      expect(frames.filter(frame => frame.type === 'CORTEX_STEP_TYPE_USER_INPUT' && pickString(pickObject(frame, 'userInput'), 'userResponse').includes(task))).toHaveLength(0)
      expect(frames.filter(frame => typeof frame.content === 'string' && frame.content.includes(task))).toHaveLength(1)
    }
  }, async () => {
    await native.modelScript.releaseGateIfHeld(gate)
  })
})

antigravityTest('preserves native agent messages in both directions after reload', async ({ native }) => {
  const root = await currentNativeAgent(native)
  const child = await openAntigravityRunningChild(native)
  await finishAntigravityChildWithReload(native, child)
  const marker = uniqueMarker('AGYNATIVEMESSAGE')
  const directions = [
    {
      sender: root.id,
      recipient: child.childId,
      nativeRecipient: child.nativeChildId,
      label: 'parent-to-child',
    },
    {
      sender: child.childId,
      recipient: root.id,
      nativeRecipient: root.agentSessionId,
      label: 'child-to-parent',
    },
  ]
  for (const direction of directions) {
    const message = `${marker}${direction.label}`
    const prompt = `${marker} Send ${direction.label} through the native message tool.`
    const gate = `${direction.label}-${marker}`
    const recipientRule = `${direction.label} receives its native message`
    await native.modelScript.rule({
      name: `${direction.label} sends its native message`,
      when: { user: prompt },
      respond: {
        toolCalls: [
          antigravitySendMessageToolCall(`${direction.label}-${marker}`, direction.nativeRecipient, message),
        ],
      },
      once: true,
    }, {
      name: recipientRule,
      when: { user: message },
      respond: { text: `Received ${direction.label}.`, gate },
      once: true,
    })
    await withCleanup(async () => {
      await native.page.locator(`${AGENT_TAB_SELECTOR}${tabIdSelector(direction.sender)}:visible`).click()
      await waitForAgentIdle(native.page)
      await sendMessage(native.page, native.modelScript.prompt(prompt))
      const held = await native.modelScript.waitForGate(gate)
      expect(nativeModelContextText(ruleRequest(held, recipientRule))).toContain(message)
      // Root activity includes the child, so waiting here would block the parent-to-child case.
      await native.page.locator(`${AGENT_TAB_SELECTOR}${tabIdSelector(direction.recipient)}:visible`).click()
      await expect(messageContents(native.page).filter({ hasText: message })).toHaveCount(1)
      await native.modelScript.releaseGate(gate)
      await waitForAgentIdle(native.page)
      for (const reload of [false, true]) {
        if (reload)
          await native.page.reload()
        await expect(messageContents(native.page).filter({ hasText: message })).toHaveCount(1)
        const snapshot = await readNativeMessageSnapshot(native, direction.recipient)
        const messages = snapshot.messages.map(nativeMessageBody).filter(isObject).map(frame => pickObject(pickObject(frame, 'systemMessage'), 'agentMessage')).filter(frame => pickString(frame, 'content').includes(message))
        expect(messages).toHaveLength(1)
        expect(pickString(messages[0], 'sender')).not.toBe('')
      }
    }, async () => { await native.modelScript.releaseGateIfHeld(gate) })
  }
})
