import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codexTest } from '../codex-fixtures'
import { clearGoal, expandGoalsAndTodosSection, expectEmptyGoalCard, expectGoalStatus, goalAction, openGoalMenu, submitGoal } from '../helpers/goalsAndTodos'
import { sendNativeAnswer } from '../helpers/nativeConversation'

codexTest('pauses and resumes an acknowledged native goal and restores its paused state', async ({ authenticatedCodexWorkspace, page, modelScript }) => {
  void authenticatedCodexWorkspace
  await sendNativeAnswer({ page, modelScript, provider: AgentProvider.CODEX }, 'Start this native goal session.', 'The native goal session is ready.')
  const gate = 'codex-paused-goal-model'
  await modelScript.fallback({ text: 'The held goal turn ended.', gate })
  await submitGoal(page, modelScript.prompt('Keep this objective until I clear it.'))
  await modelScript.waitForGate(gate)
  try {
    await expectGoalStatus(page, 'active')
    await openGoalMenu(page)
    await goalAction(page, 'pause').click()
    await expectGoalStatus(page, 'paused')
    await page.reload()
    await expandGoalsAndTodosSection(page)
    await expectGoalStatus(page, 'paused')
    await openGoalMenu(page)
    await goalAction(page, 'resume').click()
    await expectGoalStatus(page, 'active')
    await clearGoal(page)
    await expectEmptyGoalCard(page)
  }
  finally {
    if ((await modelScript.status()).pendingGates.includes(gate))
      await modelScript.releaseGate(gate)
  }
})
