import { COPILOT_MODE, COPILOT_OPTION, COPILOT_PERMISSION_MODE } from '../../src/generated/contracts/copilot-protocol'
import { COPILOT_E2E_SKIP_REASON, copilotTest, expect } from './copilot-fixtures'
import { expandGoalsAndTodosSection, expectGoalStatus, goalAction, openGoalMenu } from './helpers/subagentRegistry'
import { applyPermissionPreset, ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, expectAssistantAnswer, openSettingsMenu, sendMessage, waitForAgentIdle } from './helpers/ui'

copilotTest.skip(!!COPILOT_E2E_SKIP_REASON, COPILOT_E2E_SKIP_REASON || '')

copilotTest.describe('Copilot Basic Chat', () => {
  copilotTest('send message and receive response', async ({ authenticatedCopilotWorkspace, page, modelScript }) => {
    void authenticatedCopilotWorkspace
    await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await waitForAgentIdle(page, 120_000)
    await expectAssistantAnswer(page)
  })

  // The native runtime carries two independent axes. The presets move the permission
  // mode, and the session mode stays where it was.
  copilotTest('permission presets switch the native permission mode', async ({ authenticatedCopilotWorkspace, page }) => {
    void authenticatedCopilotWorkspace
    const checked = async (value: string) => {
      const group = await openSettingsMenu(page, 'permissionMode')
      return group.locator(`[data-testid="permissionMode-${value}"] input[type="radio"]`)
    }
    // MANUAL, not Assisted. The fixture opens every Copilot agent with an
    // explicit `permissionMode: manual`, and an explicit request beats the
    // provider's new-session default (`resolveLaunchOptions`), so the session
    // starts there. Asserting Assisted made this test contradict its own
    // fixture -- the presets below are what this test is actually for.
    await expect(await checked(COPILOT_PERMISSION_MODE.Manual)).toBeChecked()

    await applyPermissionPreset(page, 'bypass')
    await expect(await checked(COPILOT_PERMISSION_MODE.AllowAll)).toBeChecked()

    await applyPermissionPreset(page, 'smart')
    await expect(await checked(COPILOT_PERMISSION_MODE.Assisted)).toBeChecked()

    const modes = await openSettingsMenu(page, COPILOT_OPTION.SessionMode)
    await expect(modes.locator(`[data-testid="${COPILOT_OPTION.SessionMode}-${COPILOT_MODE.Interactive}"] input[type="radio"]`)).toBeChecked()
    for (const mode of [COPILOT_MODE.Plan, COPILOT_MODE.Autopilot])
      await expect(modes.locator(`[data-testid="${COPILOT_OPTION.SessionMode}-${mode}"]`)).toBeVisible()
  })

  /**
   * The native objective, through its own commands.
   *
   * Copilot records the objective inside the command and returns the prompt that
   * pursues it, so the queue is paused for the whole test: the objective is stored
   * either way, and no model turn runs.
   */
  copilotTest('sets, pauses and clears a native session goal', async ({ authenticatedCopilotWorkspace, page }) => {
    void authenticatedCopilotWorkspace
    const objective = 'Keep the native Copilot objective until the browser clears it.'
    const queue = page.locator('[data-testid="agent-input-queue"]:visible')
    const pauseButton = page.locator('[data-testid="queue-pause-button"]:visible')

    await expandGoalsAndTodosSection(page)
    await pauseButton.click()
    await goalAction(page, 'set').click()
    await page.locator('[data-testid="goal-editor"]:visible .ProseMirror').fill(objective)
    await page.locator('[data-testid="set-goal-submit"]:visible').click()

    await expect(page.locator('[data-testid="goal-objective"]:visible')).toContainText(objective)
    await expectGoalStatus(page, 'active')
    // The runtime's own continuation prompt waits in the queue. LeapMux never writes
    // one of its own, so a queue with nothing in it would mean the effect was lost.
    await expect(queue).toBeVisible()

    await openGoalMenu(page)
    await goalAction(page, 'pause').click()
    await expectGoalStatus(page, 'paused')
    await expect(page.locator('[data-testid="goal-objective"]:visible')).toContainText(objective)

    // The objective survives a reload, because the runtime stores it.
    await page.reload()
    await expandGoalsAndTodosSection(page)
    await expect(page.locator('[data-testid="goal-objective"]:visible')).toContainText(objective)

    await openGoalMenu(page)
    await goalAction(page, 'clear').click()
    await expect(page.locator('[data-testid="goal-card-empty"]:visible')).toBeVisible()
  })
})
