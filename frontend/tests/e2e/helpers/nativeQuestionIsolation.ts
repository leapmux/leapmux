import type { ManagedNativeScenarioContext } from './nativeScenario'
import { expect } from '@playwright/test'
import { createWorkspaceViaAPI, getUserId } from './api'
import { withCleanup } from './cleanup'
import { watchNativeControls } from './nativeControlWatch'
import { askQuestions, NATIVE_CONTROL_QUESTION, restoreQuestionTextDraft } from './nativeQuestion'
import { currentNativeAgent } from './nativeScenario'
import { waitForOneNativeControl } from './nativeStoredControlDecision'
import { collapseWorkspaceRow, composerEditor, controlAgentId, controlBanner, expectNoControlBanner, openWorkspace, sidebarLeaves, tabById, waitForAgentIdle, waitForControlBanner, waitForEditorDraft, waitForWorkspaceReady, workspaceRow, workspaceRowTitle } from './ui'
import { openProviderAgent } from './workspace'

/** Verify that a real native question belongs to its tab and workspace, including its stored draft. */
export async function exerciseQuestionIsolation(
  context: ManagedNativeScenarioContext,
  options: { customAnswerDraft?: boolean, dismiss?: () => Promise<void> } = {},
): Promise<void> {
  const { page, leapmuxServer: server, modelScript, workspaceId } = context
  const owner = await currentNativeAgent(context)
  const second = await openProviderAgent(server, workspaceId, context.providerAgent)
  const otherWorkspace = await createWorkspaceViaAPI(server.hubUrl, server.adminToken, 'Other question workspace')
  await openProviderAgent(server, otherWorkspace, context.providerAgent)
  await openWorkspace(page, otherWorkspace)
  await waitForWorkspaceReady(page)
  await openWorkspace(page, workspaceId)
  const ownerTab = tabById(page, owner.id)
  const secondTab = tabById(page, second.agentId)
  await ownerTab.click()
  const watch = await watchNativeControls(server, owner.id)
  const gate = 'question-owner-background'
  const userId = server.adminUserId ?? await getUserId(server.hubUrl, server.adminToken)
  const conversationDraft = 'Keep this conversation draft while the question waits.'
  const questionDraft = 'Keep this separate native question answer.'
  await withCleanup(async () => {
    await askQuestions(context, [NATIVE_CONTROL_QUESTION], { gate })
    await composerEditor(page).fill(conversationDraft)
    await waitForEditorDraft(page, userId, conversationDraft)
    await secondTab.click()
    await expect(secondTab).toHaveAttribute('aria-selected', 'true')
    await modelScript.releaseGate(gate)
    const observed = await waitForOneNativeControl(watch)
    expect(observed.requestId).not.toBe('')
    const tabBadge = ownerTab.getByTestId('tab-notification')
    await expect(tabBadge).toBeVisible()
    await expect(controlBanner(page)).toHaveCount(0)

    await workspaceRowTitle(page, otherWorkspace).click()
    await waitForWorkspaceReady(page)
    const sidebarBadge = '[data-testid="sidebar-tab-notification"]'
    await expect(sidebarLeaves(page, workspaceId).locator(sidebarBadge)).toHaveCount(1)
    await collapseWorkspaceRow(page, workspaceId)
    await expect(workspaceRow(page, workspaceId).locator(sidebarBadge)).toBeVisible()
    await workspaceRowTitle(page, workspaceId).click()
    await waitForWorkspaceReady(page)
    await expect(secondTab).toHaveAttribute('aria-selected', 'true')
    await expect(tabBadge).toBeVisible()
    await expect(controlBanner(page)).toHaveCount(0)
    await expect(workspaceRow(page, workspaceId)).toHaveAttribute('data-expanded', 'true')
    await expect(workspaceRow(page, workspaceId).locator(sidebarBadge)).toHaveCount(0)
    await expect(sidebarLeaves(page, workspaceId).locator(sidebarBadge)).toHaveCount(1)

    await ownerTab.click()
    await expect(tabBadge).not.toBeVisible()
    const banner = await waitForControlBanner(page)
    expect(await controlAgentId(banner)).toBe(owner.id)
    await expect(banner).toContainText(NATIVE_CONTROL_QUESTION.question)
    if (options.customAnswerDraft) {
      await expect(composerEditor(page)).toHaveText('')
      await composerEditor(page).fill(questionDraft)
      await restoreQuestionTextDraft(context, questionDraft)
      await secondTab.click()
      await expect(controlBanner(page)).toHaveCount(0)
      await expect(composerEditor(page)).not.toHaveText(questionDraft)
      await ownerTab.click()
      await expect(composerEditor(page)).toHaveText(questionDraft)
    }
    else {
      await page.reload()
      expect(await controlAgentId(await waitForControlBanner(page))).toBe(owner.id)
      await expect(controlBanner(page)).toContainText(NATIVE_CONTROL_QUESTION.question)
    }
    if (options.dismiss)
      await options.dismiss()
    else
      await controlBanner(page).getByTestId('control-interrupt').click()
    await expectNoControlBanner(page)
    await waitForAgentIdle(page)
    await expect(composerEditor(page)).toHaveText(conversationDraft)
  }, async () => {
    watch.cancel()
    await modelScript.releaseGateIfHeld(gate)
  })
}
