import { codebuddyTest, expect } from '../codebuddy-fixtures'
import { expandGoalsAndTodosSection, expectEmptyGoalCard, expectGoalObjective, expectGoalStatus, goalAction, openGoalMenu, submitGoal } from '../helpers/goalsAndTodos'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { waitForAgentIdle, waitForSettingsHydrated } from '../helpers/ui'

codebuddyTest.describe('CodeBuddy Code session goal', () => {
  codebuddyTest('sets and clears a goal through native commands', async ({ native }) => {
    const { page, modelScript } = native
    await waitForSettingsHydrated(page, 'permissionMode')
    await modelScript.rule({
      name: 'native goal command turn',
      when: { user: '<user_query>/goal' },
      respond: { text: 'The goal command was processed.' },
    })
    await sendNativeAnswer(native, 'Reply once before the goal command.', 'Goal controls are ready.')
    await submitGoal(page, 'Keep the example objective active.')
    await expectGoalStatus(page, 'active')
    await expectGoalObjective(page, 'Keep the example objective active.')

    await openGoalMenu(page)
    await expect(goalAction(page, 'pause')).toHaveCount(0)
    await expect(goalAction(page, 'resume')).toHaveCount(0)
    await goalAction(page, 'clear').click()
    await expectEmptyGoalCard(page)
    await waitForAgentIdle(page)
    expect((await modelScript.status()).ruleMatches['native goal command turn']).toBeGreaterThan(1)

    await page.reload()
    await expandGoalsAndTodosSection(page)
    await expectEmptyGoalCard(page)
  })
})
