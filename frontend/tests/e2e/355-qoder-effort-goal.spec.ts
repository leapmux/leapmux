import { expandGoalsAndTodosSection, expectGoalStatus, goalAction, openGoalMenu } from './helpers/subagentRegistry'
import { chooseSettingsOption, sendMessage, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from './helpers/ui'
import { expect, QODER_E2E_SKIP_REASON, qoderTest } from './qoder-fixtures'

qoderTest.skip(!!QODER_E2E_SKIP_REASON, QODER_E2E_SKIP_REASON || '')

qoderTest.describe('Qoder CLI effort and session goal', () => {
  qoderTest('uses the selected reasoning effort in the next native model request', async ({ qoderWorkspace, page, modelScript }) => {
    void qoderWorkspace
    await waitForSettingsHydrated(page)
    await modelScript.queue({ text: 'PRIOR_QODER_EFFORT_CONTEXT' })
    await sendMessage(page, modelScript.prompt('Remember the effort context marker.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    await chooseSettingsOption(page, 'effort-low')
    await waitForSettingsIdle(page)
    await expect(page.locator('[data-testid="composer-effort-trigger"]:visible')).toContainText('Low')

    await modelScript.queue({ text: 'LOW_EFFORT_APPLIED' })
    await sendMessage(page, modelScript.prompt('Reply once using the selected effort.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)
    const request = status.requests.find(item => item.stepIndex === 1)
    expect(request).toBeDefined()
    const body = JSON.stringify(request?.body)
    expect(body.includes('"reasoning_effort":"low"')).toBe(true)
    expect(body.includes('PRIOR_QODER_EFFORT_CONTEXT')).toBe(true)

    await page.reload()
    await expect(page.locator('[data-testid="composer-effort-trigger"]:visible')).toContainText('Low')
  })

  qoderTest('sets, pauses, resumes, and clears a native session goal', async ({ qoderWorkspace, page, modelScript }) => {
    void qoderWorkspace
    await expandGoalsAndTodosSection(page)
    await expect(page.locator('[data-testid="goal-card-empty"]:visible')).toBeVisible()
    await goalAction(page, 'set').click()
    const objective = 'Keep the Qoder goal until I clear it.'
    await page.locator('[data-testid="goal-editor"]:visible .ProseMirror').fill(objective)
    await page.locator('[data-testid="set-goal-submit"]:visible').click()
    await expect(page.locator('[data-testid="goal-objective"]:visible')).toContainText(objective)
    await expectGoalStatus(page, 'active')

    await openGoalMenu(page)
    await goalAction(page, 'pause').click()
    await expectGoalStatus(page, 'paused')
    await page.reload()
    await expandGoalsAndTodosSection(page)
    await expect(page.locator('[data-testid="goal-objective"]:visible')).toContainText(objective)
    await expectGoalStatus(page, 'paused')

    await openGoalMenu(page)
    await goalAction(page, 'resume').click()
    await expectGoalStatus(page, 'active')
    await openGoalMenu(page)
    await goalAction(page, 'clear').click()
    await expect(page.locator('[data-testid="goal-card-empty"]:visible')).toBeVisible()
    expect((await modelScript.status()).requests).toHaveLength(0)
  })
})
