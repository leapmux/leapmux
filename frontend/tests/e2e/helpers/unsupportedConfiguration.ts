import type { ManagedNativeScenarioContext } from './nativeScenario'
import { Code } from '@connectrpc/connect'
import { expect } from '@playwright/test'
import { AgentGoalAction, AgentStatus, ListAgentMessagesRequestSchema, ListAgentMessagesResponseSchema, UpdateAgentGoalRequestSchema, UpdateAgentGoalResponseSchema } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { getTestChannel } from './api'
import { currentNativeAgent } from './nativeScenario'
import { expandGoalsAndTodosSection, goalAction, goalsAndTodosSection, openGoalMenu } from './subagentRegistry'
import { expectPermissionShortcuts, openPlusMenu, settingsGroupTrigger, waitForNativeSettingsHydrated } from './ui'

interface RelatedNativeProof {
  relatedProof: () => Promise<void>
}

/** Check a missing setting against the live catalog and a working native operation. */
export async function expectMissingOptionGroup(
  context: ManagedNativeScenarioContext,
  options: RelatedNativeProof & { groupId: string },
): Promise<void> {
  await options.relatedProof()
  await waitForNativeSettingsHydrated(context.page)
  const agent = await currentNativeAgent(context)
  expect(agent.status).toBe(AgentStatus.ACTIVE)
  expect(agent.optionGroups.length).toBeGreaterThan(0)
  expect(agent.optionGroups.map(group => group.id)).not.toContain(options.groupId)
  await openPlusMenu(context.page)
  await expect(settingsGroupTrigger(context.page, options.groupId)).toHaveCount(0)
  await context.page.keyboard.press('Escape')
  await context.page.reload()
  await waitForNativeSettingsHydrated(context.page)
  const restored = await currentNativeAgent(context)
  expect(restored.optionGroups.map(group => group.id)).not.toContain(options.groupId)
  await openPlusMenu(context.page)
  await expect(settingsGroupTrigger(context.page, options.groupId)).toHaveCount(0)
  await context.page.keyboard.press('Escape')
}

/** Check a missing permission preset after a real native permission or tool operation. */
export async function expectMissingPermissionShortcut(
  context: ManagedNativeScenarioContext,
  options: RelatedNativeProof & { preset: 'smart' | 'bypass' },
): Promise<void> {
  await options.relatedProof()
  await waitForNativeSettingsHydrated(context.page)
  const agent = await currentNativeAgent(context)
  expect(agent.status).toBe(AgentStatus.ACTIVE)
  expect(agent.optionGroups.length).toBeGreaterThan(0)
  const absent = options.preset === 'smart' ? { smart: 'absent' as const } : { bypass: 'absent' as const }
  await expectPermissionShortcuts(context.page, absent)
  await context.page.reload()
  await waitForNativeSettingsHydrated(context.page)
  await expectPermissionShortcuts(context.page, absent)
}

const GOAL_ACTION_WORDS = new Map<AgentGoalAction, 'set' | 'clear' | 'pause' | 'resume'>([
  [AgentGoalAction.SET, 'set'],
  [AgentGoalAction.CLEAR, 'clear'],
  [AgentGoalAction.PAUSE, 'pause'],
  [AgentGoalAction.RESUME, 'resume'],
])

/** Refuse only the goal actions that the actual running provider omits. */
export async function expectUnsupportedGoalActions(
  context: ManagedNativeScenarioContext,
  options: RelatedNativeProof & { actions: readonly AgentGoalAction[], objective?: string },
): Promise<void> {
  if (options.actions.length === 0)
    throw new Error('the negative goal proof requires an action')
  await options.relatedProof()
  const agent = await currentNativeAgent(context)
  expect(agent.status).toBe(AgentStatus.ACTIVE)
  const channel = await getTestChannel(context.leapmuxServer.hubUrl, context.leapmuxServer.adminToken)
  const before = await channel.callWorker(
    context.leapmuxServer.workerId,
    'ListAgentMessages',
    ListAgentMessagesRequestSchema,
    ListAgentMessagesResponseSchema,
    { agentId: agent.id, limit: 1 },
  )
  expect(before.goalLoaded).toBe(true)
  if (await goalsAndTodosSection(context.page).count() > 0)
    await expandGoalsAndTodosSection(context.page)
  const menu = context.page.locator('[data-testid="goal-actions-trigger"]:visible')
  if (await menu.count() > 0)
    await openGoalMenu(context.page)
  for (const action of options.actions) {
    const word = GOAL_ACTION_WORDS.get(action)
    if (!word)
      throw new Error('the negative goal proof received an unspecified action')
    expect(before.goalSupportedActions).not.toContain(action)
    await expect(goalAction(context.page, word)).toHaveCount(0)
    await expect(channel.callWorker(
      context.leapmuxServer.workerId,
      'UpdateAgentGoal',
      UpdateAgentGoalRequestSchema,
      UpdateAgentGoalResponseSchema,
      { agentId: agent.id, action, objective: options.objective ?? 'Keep this negative goal probe until the operator clears it.' },
    )).rejects.toMatchObject({
      source: 'rpc',
      code: Code.FailedPrecondition,
      message: 'this agent cannot perform that session-goal action',
    })
  }
  await context.page.keyboard.press('Escape')
  const after = await channel.callWorker(
    context.leapmuxServer.workerId,
    'ListAgentMessages',
    ListAgentMessagesRequestSchema,
    ListAgentMessagesResponseSchema,
    { agentId: agent.id, limit: 1 },
  )
  expect(after.goalSupportedActions).toEqual(before.goalSupportedActions)
  expect(after.goal).toEqual(before.goal)
}
