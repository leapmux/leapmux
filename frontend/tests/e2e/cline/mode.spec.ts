import type { NativeModeNotice } from '../helpers/nativeModeChange'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { Buffer } from 'node:buffer'
import { expect } from '@playwright/test'
import { CLINE_PERMISSION_MODE } from '../../../src/generated/contracts/cline-protocol'
import { AgentActivityState } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { clineTest } from '../cline-fixtures'
import { watchAgentEvents } from '../helpers/agentEventWatch'
import { withCleanup } from '../helpers/cleanup'
import { readNativeInputQueue, waitForNativeInputQueueIdle } from '../helpers/nativeInputQueueIdle'
import { captureStoredNativeModeNotice } from '../helpers/nativeModeChange'
import { currentNativeAgent, nativeOptionValue } from '../helpers/nativeScenario'
import { expectSettingsOptionsOffered } from '../helpers/nativeSettings'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { queuedInputRow } from '../helpers/steer'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, assistantBubbles, chooseSettingsOption, expectAssistantAnswer, expectSettingsChip, inputQueue, SECOND_ARITHMETIC_ANSWER, SECOND_ARITHMETIC_ANSWER_TEXT, SECOND_ARITHMETIC_PROMPT, sendMessage, visibleOnly, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { offeredTools } from './offeredTools'
import { nativeContext } from './scenarios'

/** Cline publishes WORKING during a protected rebuild. The rebuild sends no model request and finishes at IDLE. */
async function chooseSettledMode(context: ManagedNativeScenarioContext, value: string, previousLabel: string, label: string): Promise<NativeModeNotice> {
  const { page, modelScript } = context
  const ownerBefore = await currentNativeAgent(context)
  const oldValue = nativeOptionValue(ownerBefore, 'permissionMode')
  if (!oldValue)
    throw new Error('The Cline mode change requires a current native permission mode.')
  const baseline = await waitForNativeInputQueueIdle(context.leapmuxServer, ownerBefore.id, modelScript.testDeadline)
  await expect(page.locator('[data-testid="thinking-indicator"]:visible')).toHaveCount(0)
  const before = (await modelScript.status()).requests.length
  const watch = await watchAgentEvents(context.leapmuxServer, ownerBefore.id, {
    label: 'Cline mode settling',
    select: event => !event.replay && (event.event.case === 'inputQueueChanged' || event.event.case === 'activityChanged') ? event : undefined,
  })
  return withCleanup(async () => {
    await chooseSettingsOption(page, `permissionMode-${value}`)
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, label)
    const queues = () => watch.items().flatMap((event) => {
      if (event.event.case !== 'inputQueueChanged')
        return []
      const snapshot = event.event.value.snapshot
      if (!snapshot || snapshot.agentId !== ownerBefore.id)
        throw new Error('The Cline settling event requires the captured owner input queue.')
      return [snapshot]
    })
    // WatchEvents permits one stream per channel. A second idle subscription would replace this live activity watch.
    await expect.poll(() => queues().some(snapshot => !snapshot.activeTurn && snapshot.revision > baseline.revision)).toBe(true)
    const settled = await readNativeInputQueue(context.leapmuxServer, ownerBefore.id)
    expect(settled.activeTurn).toBe(false)
    expect(settled.revision).toBeGreaterThan(baseline.revision)
    await expect.poll(() => queues().some(snapshot => !snapshot.activeTurn && snapshot.revision >= settled.revision)).toBe(true)
    const changedQueues = queues().filter(snapshot => snapshot.revision > baseline.revision)
    const busyIndex = changedQueues.findIndex(snapshot => snapshot.activeTurn && !snapshot.activeTurnSteerable)
    expect(busyIndex).toBeGreaterThanOrEqual(0)
    const busy = changedQueues[busyIndex]
    if (!busy)
      throw new Error('The native Cline rebuild did not hold its input queue.')
    expect({ paused: busy.paused, itemCount: busy.items.length }).toEqual({ paused: false, itemCount: 0 })
    expect(changedQueues.slice(busyIndex + 1).some(snapshot => !snapshot.activeTurn && snapshot.revision > busy.revision)).toBe(true)

    const activity = () => watch.items().flatMap(event => event.event.case === 'activityChanged' ? [event.event.value.state] : [])
    await expect.poll(activity).toEqual(expect.arrayContaining([AgentActivityState.WORKING, AgentActivityState.IDLE]))
    const workingIndex = activity().indexOf(AgentActivityState.WORKING)
    expect(activity().slice(workingIndex + 1)).toContain(AgentActivityState.IDLE)
    const status = await modelScript.status()
    expect(status.requests).toHaveLength(before)
    await clineTest.info().attach(`native-cline-mode-settling-${value}`, {
      body: Buffer.from(JSON.stringify({
        agentId: ownerBefore.id,
        mode: value,
        baselineRevision: baseline.revision.toString(),
        queues: changedQueues.map(snapshot => ({
          revision: snapshot.revision.toString(),
          activeTurn: snapshot.activeTurn,
          activeTurnSteerable: snapshot.activeTurnSteerable,
          paused: snapshot.paused,
          itemCount: snapshot.items.length,
        })),
        activity: activity(),
        modelRequestsBefore: before,
        modelRequestsAfter: status.requests.length,
      })),
      contentType: 'application/json',
    })
    await expect(page.locator('[data-testid="thinking-indicator"]:visible')).toHaveCount(0)
    const notice = visibleOnly(page.getByText(`Mode (${previousLabel} → ${label})`, { exact: true }))
    await expect(notice).toHaveCount(1)
    return captureStoredNativeModeNotice(context, ownerBefore, { groupId: 'permissionMode', oldValue, value, label, previousLabel, noticeLabel: 'Mode' }, notice)
  }, async () => {
    watch.cancel()
  })
}

/**
 * The mode choice must reach the actual native session. The browser must follow native changes and refusal limits.
 *
 * The Worker starts one private Cline hub for this agent. Cline's DeepSeek provider sends requests to the isolated mock.
 *
 * Cline fixes tools and system instructions when it creates a session. A Plan or Act change recreates the same session with its earlier messages.
 */
clineTest.describe('Cline settings', () => {
  clineTest('moves the session between Act and Plan, and keeps the conversation', async ({ askingClineWorkspace, page, modelScript, leapmuxServer }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingClineWorkspace.workspaceId })
    await waitForSettingsHydrated(page)
    await expectSettingsOptionsOffered(page, 'permissionMode', Object.values(CLINE_PERMISSION_MODE))
    const actStep = await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps(actStep + 1)
    await waitForAgentIdle(page)
    await expectAssistantAnswer(page)

    const planNotice = await chooseSettledMode(context, CLINE_PERMISSION_MODE.Plan, 'Act', 'Plan')

    const planStep = await modelScript.queue({ text: SECOND_ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(SECOND_ARITHMETIC_PROMPT))
    await modelScript.waitForSteps(planStep + 1)
    await waitForAgentIdle(page)
    await expectAssistantAnswer(page, { answer: SECOND_ARITHMETIC_ANSWER })

    await page.reload()
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Plan')
    const restoredPlanStep = await modelScript.queue({ text: 'The restored setting answered.' })
    await sendMessage(page, modelScript.prompt('Reply once after the reload.'))
    await modelScript.waitForSteps(restoredPlanStep + 1)
    await waitForAgentIdle(page)
    const restoredPlan = (await modelScript.requestAt(restoredPlanStep)).body
    expect(offeredTools(restoredPlan)).toContain('switch_to_act_mode')
    expect(offeredTools(restoredPlan)).not.toContain('editor')

    const actNotice = await chooseSettledMode(context, CLINE_PERMISSION_MODE.Act, 'Plan', 'Act')
    await page.reload()
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Act')

    const actAgainStep = await modelScript.queue({ text: 'Back in Act.' })
    await sendMessage(page, modelScript.prompt('Reply with the words: Back in Act.'))
    await modelScript.waitForSteps(actAgainStep + 1)
    await waitForAgentIdle(page)

    const [act, plan, actAgain] = await Promise.all([actStep, planStep, actAgainStep].map(async step => (await modelScript.requestAt(step)).body))
    // Act offers the editor. Plan offers the plan tool in its place.
    expect(offeredTools(act)).toContain('editor')
    expect(offeredTools(act)).not.toContain('switch_to_act_mode')
    expect(offeredTools(plan)).toContain('switch_to_act_mode')
    expect(offeredTools(plan)).not.toContain('editor')
    expect(offeredTools(actAgain)).toContain('editor')
    expect(offeredTools(actAgain)).not.toContain('switch_to_act_mode')
    // Each new session holds the conversation of the session before it.
    expect(JSON.stringify(plan)).toContain(ARITHMETIC_ANSWER_TEXT)
    expect(JSON.stringify(actAgain)).toContain(ARITHMETIC_ANSWER_TEXT)
    expect(JSON.stringify(actAgain)).toContain(SECOND_ARITHMETIC_ANSWER_TEXT)
    await planNotice.assertStored()
    await actNotice.assertStored()
  })

  clineTest('keeps queued input until the native mode change settles', async ({ askingClineWorkspace, page, modelScript, leapmuxServer }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingClineWorkspace.workspaceId })
    await waitForSettingsHydrated(page)
    const { id } = await currentNativeAgent(context)
    const gate = 'cline-mode-first-answer'
    const nextPrompt = 'Answer the queued request after the setting applies.'
    const start = await modelScript.queue(
      { text: 'The first native turn ended.', gate },
      { text: 'The queued native turn used the new setting.' },
    )
    const changeBefore = await withCleanup(async () => {
      await sendMessage(page, modelScript.prompt('Answer the first request.'))
      await modelScript.waitForGate(gate)
      const ownerBefore = await currentNativeAgent(context)
      const oldValue = nativeOptionValue(ownerBefore, 'permissionMode')
      if (!oldValue)
        throw new Error('The Cline mode change requires a current native permission mode.')
      await chooseSettingsOption(page, 'permissionMode-plan')
      await expectSettingsChip(page, 'Plan')
      await expect(page.locator('[data-testid="settings-loading-spinner"]')).toBeVisible()
      // ListAgents overlays the requested mode. The held request still uses Act's native tools until its turn ends.
      const heldRequest = await modelScript.requestAt(start)
      expect(offeredTools(heldRequest.body)).toContain('editor')
      expect(offeredTools(heldRequest.body)).not.toContain('switch_to_act_mode')
      await sendMessage(page, modelScript.prompt(nextPrompt))
      await expect(queuedInputRow(page, nextPrompt)).toBeVisible()
      await retryUntilPass(async () => {
        const heldQueue = await readNativeInputQueue(leapmuxServer, id)
        expect({ activeTurn: heldQueue.activeTurn, paused: heldQueue.paused, itemCount: heldQueue.items.length })
          .toEqual({ activeTurn: true, paused: false, itemCount: 1 })
      })
      expect((await modelScript.status()).requests).toHaveLength(1)
      return { ownerBefore, oldValue }
    }, async () => { await modelScript.releaseGateIfHeld(gate) })

    await modelScript.waitForSteps(start + 2)
    await waitForSettingsIdle(page)
    await waitForNativeInputQueueIdle(leapmuxServer, id, modelScript.testDeadline)
    await expectSettingsChip(page, 'Plan')
    await expect(inputQueue(page)).toHaveCount(0)
    await expect(assistantBubbles(page).filter({ hasText: 'The queued native turn used the new setting.' }).first()).toBeVisible()
    const first = await modelScript.requestAt(start)
    const next = await modelScript.requestAt(start + 1)
    expect(offeredTools(first.body)).toContain('editor')
    expect(offeredTools(first.body)).not.toContain('switch_to_act_mode')
    expect(offeredTools(next.body)).toContain('switch_to_act_mode')
    expect(offeredTools(next.body)).not.toContain('editor')
    expect(JSON.stringify(first.body)).not.toContain(nextPrompt)
    expect(JSON.stringify(next.body)).toContain(nextPrompt)
    expect(JSON.stringify(next.body)).toContain('The first native turn ended.')
    expect((await modelScript.status()).requests).toHaveLength(2)
    const notice = visibleOnly(page.getByText('Mode (Act → Plan)', { exact: true }))
    await expect(notice).toHaveCount(1)
    const storedNotice = await captureStoredNativeModeNotice(context, changeBefore.ownerBefore, {
      groupId: 'permissionMode',
      oldValue: changeBefore.oldValue,
      value: CLINE_PERMISSION_MODE.Plan,
      label: 'Plan',
      previousLabel: 'Act',
      noticeLabel: 'Mode',
    }, notice)
    await storedNotice.assertStored()
    await expect(page.locator('[data-testid="thinking-indicator"]:visible')).toHaveCount(0)
  })
})
