import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { agentOpenOptions } from '../agentSettings'
import { openAgentViaAPI } from '../helpers/api'
import { newNativeWorkingDir } from '../helpers/nativeAgentOpen'
import { nativeAgentById } from '../helpers/nativeScenario'
import { exitPlanModeToolCall } from '../helpers/providerToolCalls'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { answerPlanReview, chatScrollContainer, openWorkspace, sendMessage, waitForControlBanner } from '../helpers/ui'

/**
 * Approve a Pi plan with the clear-context choice, and prove that Pi starts a fresh native session that runs the plan.
 * The scenario opens its own agent in the workspace of the context.
 */
export async function exercisePiFreshPlanSession(context: ManagedNativeScenarioContext): Promise<void> {
  const { page, modelScript, leapmuxServer } = context
  const agentId = await openAgentViaAPI(leapmuxServer, context.workspaceId, newNativeWorkingDir(context, 'renderer-pi-fresh-plan-'), agentOpenOptions(context.provider))
  const readSession = async () => (await nativeAgentById(context, agentId))?.agentSessionId ?? ''
  const originalSession = await retryUntilPass(async () => {
    const session = await readSession()
    expect(session, 'the Worker starts the native session of the new agent').not.toBe('')
    return session
  })
  await page.reload()
  await openWorkspace(page, context.workspaceId)
  await sendMessage(page, '/plan start')
  // The mock model server makes no tool call that a script does not hold, so the script gives the plan tool call.
  // The mock answers a request from the script whose marker the request holds, so the plan holds the marker of this
  // test's script:
  // - An approval with the clear-context choice starts a fresh native session.
  // - The plan is the first prompt of that session, and that prompt holds no other marker.
  // - Without the marker in the plan, the mock answers the turn that runs the plan from its ambient scenario, not from
  //   the script of this test.
  const start = await modelScript.queue(
    { toolCalls: [exitPlanModeToolCall(context.provider, 'fresh-plan', modelScript.prompt('# Fresh implementation probe\n\n- Reply with FRESH_PLAN_DONE. Do not call tools or change files.'))] },
    { text: 'FRESH_PLAN_DONE' },
  )
  await sendMessage(page, modelScript.prompt('Finish the plan.'))
  await modelScript.waitForSteps(start + 1)
  const banner = await waitForControlBanner(page)
  await expect(banner).toContainText('Plan Ready for Review')
  await page.getByTestId('plan-clear-context-checkbox').filter({ visible: true }).click()
  await answerPlanReview(page, 'approve')
  await retryUntilPass(async () => {
    const current = await readSession()
    expect(current, 'the approval with a cleared context starts a native session').not.toBe('')
    expect(current, 'the approval with a cleared context leaves the original native session').not.toBe(originalSession)
  })
  await modelScript.waitForSteps(start + 2)
  await expect(chatScrollContainer(page).getByText('FRESH_PLAN_DONE', { exact: true })).toBeVisible()
}
