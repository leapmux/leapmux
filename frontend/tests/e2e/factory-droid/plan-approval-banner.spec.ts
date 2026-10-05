import { readdirSync } from 'node:fs'

import { DROID_CONFIRMATION_TYPE, DROID_PERMISSION_OPTION, DROID_REPLY, DROID_TOOL } from '../../../src/generated/contracts/droid-protocol'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { pickString } from '../../../src/lib/jsonPick'
import { DROID_E2E_SKIP_REASON, DROID_TITLE_RULE, droidTest, expect } from '../droid-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { droidNativeSettingsUpdates } from '../helpers/droidNativeSettings'
import { watchNativeControls } from '../helpers/nativeControlWatch'
import { readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { waitForNativeOptionApplied } from '../helpers/nativeSettings'
import { onlyObservedNativeControl, readNativeStoredControlDecision } from '../helpers/nativeStoredControlDecision'
import { exitPlanModeToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, chooseSettingsOption, expectSettingsOptionChosen, savedControlAnswer, sendMessage, userBubbles, visibleControlBanner, waitForAgentIdle, waitForControlBanner, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'

droidTest.describe('Factory Droid Spec mode', () => {
  droidTest.skip(!!DROID_E2E_SKIP_REASON, DROID_E2E_SKIP_REASON || '')

  droidTest('shows the native plan review and returns to Default after approval', async ({ askingDroidWorkspace, page, modelScript, leapmuxServer }) => {
    await waitForSettingsHydrated(page)
    await chooseSettingsOption(page, 'permissionMode-spec')
    await waitForSettingsIdle(page)
    await expectSettingsOptionChosen(page, 'permissionMode-spec')
    await modelScript.rule(DROID_TITLE_RULE)
    await modelScript.queue(
      { toolCalls: [exitPlanModeToolCall(AgentProvider.DROID, 'exit-spec-1', '# Native plan\n\n- Apply the change.')] },
      { text: 'The plan was approved.' },
    )
    await sendMessage(page, modelScript.prompt('Present the native plan for approval.'))
    await modelScript.waitForSteps(1)

    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText('Proposed Plan')
    await expect(banner).toContainText('Apply the change.')
    await page.getByTestId('plan-approve-btn').click()
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(banner).toHaveCount(0)
    await expect(assistantBubbles(page).filter({ hasText: 'The plan was approved.' }).first()).toBeVisible()
    // The saved row reads Droid's own `proceed_once` reply as the plan button's word.
    await expect(savedControlAnswer(page)).toHaveText('Approve')
    await expectSettingsOptionChosen(page, 'permissionMode-default')
    await expect.poll(async () => {
      const updates = await droidNativeSettingsUpdates(leapmuxServer, askingDroidWorkspace.workspaceId)
      return updates.at(-1)?.interactionMode === 'auto' && updates.at(-1)?.autonomyLevel === 'off'
    }).toBe(true)
  })

  // Reject answers ExitSpecMode with the option `cancel` ("No, keep iterating on
  // spec"). Droid records a failed result for the call and ends the turn with no
  // further model request, because a rejected tool ends an interactive turn. It stays
  // in Spec mode and changes no setting. So this script queues the tool call alone.
  droidTest('rejects the native plan review, ends the turn, and stays in Spec mode', async ({ askingDroidWorkspace, page, modelScript, leapmuxServer }) => {
    await waitForSettingsHydrated(page)
    await chooseSettingsOption(page, 'permissionMode-spec')
    await waitForSettingsIdle(page)
    await expectSettingsOptionChosen(page, 'permissionMode-spec')
    const agent = await waitForNativeOptionApplied({ page, leapmuxServer }, 'permissionMode', 'spec')
    const watch = await watchNativeControls(leapmuxServer, agent.id)
    await withCleanup(async () => {
      await modelScript.rule(DROID_TITLE_RULE)
      await modelScript.queue({
        toolCalls: [exitPlanModeToolCall(AgentProvider.DROID, 'exit-spec-rejected', '# Native plan\n\n- Keep this plan unapproved.')],
      })
      await sendMessage(page, modelScript.prompt('Present the native plan for approval.'))
      await modelScript.waitForSteps(1)
      const banner = visibleControlBanner(page)
      await expect(banner).toContainText('Proposed Plan')
      await expect.poll(() => watch.controls().length).toBeGreaterThan(0)
      const observed = onlyObservedNativeControl(watch.controls())
      await page.getByTestId('plan-reject-btn').filter({ visible: true }).click()

      await expect(banner).toHaveCount(0)
      // The browser draws the saved answer from the Worker row alone, so the row exists now.
      await expect(savedControlAnswer(page)).toHaveCount(1)
      await waitForAgentIdle(page)
      await expectSettingsOptionChosen(page, 'permissionMode-spec')
      const status = await modelScript.status()
      expect(status.unexpectedRequests).toEqual([])
      expect(status.requests.filter(request => request.stepIndex !== undefined)).toHaveLength(1)
      expect(onlyObservedNativeControl(watch.controls())).toBe(observed)
      const settings = await droidNativeSettingsUpdates(leapmuxServer, askingDroidWorkspace.workspaceId)
      expect(settings.filter(update => update.interactionMode !== undefined).at(-1)?.interactionMode).toBe('spec')

      const snapshot = await readNativeMessageSnapshot({ leapmuxServer }, agent.id)
      expect(snapshot.agentSessionId).toBe(agent.agentSessionId)
      const decision = readNativeStoredControlDecision(snapshot, observed.requestId)
      expect(decision.request).toEqual(observed.payload)
      expect(decision.request).toMatchObject({ confirmationType: DROID_CONFIRMATION_TYPE.ExitSpecMode, toolUse: { name: DROID_TOOL.ExitSpecMode } })
      expect(decision.request.options).toContain(DROID_PERMISSION_OPTION.Cancel)
      const rpcId = pickString(decision.request, 'rpcId')
      expect(rpcId).not.toBe('')
      // Droid reads a JSON-RPC response that answers the id of its own request.
      expect(decision.response).toMatchObject({ jsonrpc: '2.0', type: 'response', id: rpcId })
      expect(decision.response.result).toEqual({ [DROID_REPLY.SelectedOption]: DROID_PERMISSION_OPTION.Cancel })
      await expect(savedControlAnswer(page)).toHaveText('Reject')

      await page.reload()
      await expect(savedControlAnswer(page)).toHaveText('Reject')
      await expectSettingsOptionChosen(page, 'permissionMode-spec')
    }, async () => watch.cancel())
  })

  // A rejection with a typed reason cannot ride Droid's reply: a cancel discards
  // the `comment` for every confirmation type, and Droid's own TUI sends the
  // reason as the next user message. The worker does the same, so the reason
  // reaches the model as the reader's own next turn.
  droidTest('rejects the native plan review with a reason, which the next turn carries', async ({ askingDroidWorkspace, page, modelScript }) => {
    // A rejected plan runs nothing: the workspace content must not change.
    const before = readdirSync(askingDroidWorkspace.workingDir).sort()
    await waitForSettingsHydrated(page)
    await chooseSettingsOption(page, 'permissionMode-spec')
    await waitForSettingsIdle(page)
    await expectSettingsOptionChosen(page, 'permissionMode-spec')
    await modelScript.rule(DROID_TITLE_RULE)
    // The turn that the typed reason starts. A rule answers it, because the
    // rejection — not the script's order — decides when it arrives.
    await modelScript.rule({
      name: 'plan-rejection-feedback',
      when: { user: 'Keep the spec read-only.' },
      respond: { text: 'Understood, keeping the spec read-only.' },
    })
    await modelScript.queue(
      { toolCalls: [exitPlanModeToolCall(AgentProvider.DROID, 'exit-spec-reason', '# Native plan\n\n- Write the thing.')] },
    )
    await sendMessage(page, modelScript.prompt('Present the native plan for approval.'))
    await modelScript.waitForSteps(1)

    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText('Proposed Plan')
    const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
    await editor.click()
    await page.keyboard.type('Keep the spec read-only.', { delay: 20 })
    await page.getByTestId('plan-reject-btn').filter({ visible: true }).click()

    await expect(banner).toHaveCount(0)
    await expect(savedControlAnswer(page)).toHaveText('Reject')
    // The reason is the reader's own next message, and the model answered it.
    await expect(userBubbles(page).filter({ hasText: 'Keep the spec read-only.' }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'Understood, keeping the spec read-only.' }).first()).toBeVisible()
    await waitForAgentIdle(page)
    await expectSettingsOptionChosen(page, 'permissionMode-spec')
    const status = await modelScript.status()
    expect(status.unexpectedRequests).toEqual([])
    expect(status.ruleMatches['plan-rejection-feedback']).toBe(1)
    expect(readdirSync(askingDroidWorkspace.workingDir).sort()).toEqual(before)
  })
})
