import { expandGoalsAndTodosSection, expectGoalStatus, goalAction, openGoalMenu } from './helpers/subagentRegistry'
import { applyPermissionPreset, chooseSettingsOption, expectNoSettingsChip, expectSettingsChip, expectSettingsOptionChosen, openWorkspace, waitForSettingsHydrated, waitForSettingsIdle } from './helpers/ui'
import { expect, openQwenAgent, QWEN_E2E_SKIP_REASON, qwenTest } from './qwen-fixtures'

qwenTest.skip(!!QWEN_E2E_SKIP_REASON, QWEN_E2E_SKIP_REASON || '')

qwenTest.describe('Qwen Code settings and goal', () => {
  // Qwen's approval modes ARE its session modes, so the two presets land on the
  // permission-mode axis, and each choice survives a reload.
  qwenTest('switches the effort, the approval mode and the presets, and keeps them after reload', async ({ page, authenticatedEmptyWorkspace, leapmuxServer }) => {
    await openQwenAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Default')

    // Qwen's effort axis is its own `reasoning_effort`. The plugin declares it
    // as its effort group, so the status bar draws it as the effort chip.
    await chooseSettingsOption(page, 'reasoning_effort-low')
    await waitForSettingsIdle(page)
    await expectSettingsOptionChosen(page, 'reasoning_effort-low')
    await expectSettingsChip(page, /^low$/i)

    await chooseSettingsOption(page, 'permissionMode-auto-edit')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Auto Edit')

    await applyPermissionPreset(page, 'smart')
    await expectSettingsChip(page, /^Auto$/)
    await applyPermissionPreset(page, 'bypass')
    await expectSettingsChip(page, 'YOLO')

    await page.reload()
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'YOLO')
    await expectSettingsOptionChosen(page, 'reasoning_effort-low')

    await chooseSettingsOption(page, 'permissionMode-default')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Default')
    await expectNoSettingsChip(page, 'YOLO')
  })

  // The goal card drives Qwen's own `/goal` command. Qwen runs the goal turns
  // itself; each one reaches this script through the objective, which carries
  // the marker, and a turn that records no progress counts toward the pause.
  qwenTest('sets, follows and clears a native goal', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openQwenAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await waitForSettingsHydrated(page)
    await modelScript.rule({
      name: 'every goal turn answers DONE',
      when: { body: 'Reply with the word DONE' },
      respond: { text: 'DONE' },
    })

    await expandGoalsAndTodosSection(page)
    await goalAction(page, 'set').click()
    const objective = modelScript.prompt('Reply with the word DONE.')
    await page.locator('[data-testid="goal-editor"]:visible .ProseMirror').fill(objective)
    await page.locator('[data-testid="set-goal-submit"]:visible').click()
    await expect(page.locator('[data-testid="goal-objective"]:visible')).toContainText('Reply with the word DONE.')

    // Qwen pauses a goal after turns that record no progress, and it states why.
    await expectGoalStatus(page, 'paused')
    // A turn Qwen started by itself ends with its own notification, which draws
    // the same divider as a turn the reader started. The `/goal` prompt that set
    // the goal draws the first divider, so a second one proves that a goal round
    // drew its own.
    await expect(page.locator('[data-testid="result-divider"]:visible').nth(1)).toBeVisible()

    await openGoalMenu(page)
    await goalAction(page, 'clear').click()
    await expect(page.locator('[data-testid="goal-card-empty"]:visible')).toBeVisible()
  })
})
