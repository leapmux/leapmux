import type { MockModelRule } from '../helpers/mockModelScript'
import type { NativeGoalScenario } from '../helpers/nativeGoalLifecycle'
import { expect } from '@playwright/test'
import { AgentGoalStatus, AgentProvider, ListAgentMessagesRequestSchema, ListAgentMessagesResponseSchema } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { grokTest } from '../grok-fixtures'
import { getTestChannel } from '../helpers/api'
import { exerciseNativeGoalPauseAndResume } from '../helpers/nativeGoalLifecycle'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { writeToolCall } from '../helpers/providerToolCalls'
import { messageBubbles } from '../helpers/ui'

/** The plan that Grok's Goal Plan Writer writes, in the sections its prompt states. */
const GOAL_PLAN = [
  '# Plan: Keep the probe marker until the operator clears it',
  '',
  '## Goal kind',
  'analysis',
  '',
  '## Acceptance criteria',
  '1. native-goal-progress.txt records the first goal iteration.',
  '',
  '## Verification plan',
  '1. gating: read native-goal-progress.txt and observe the first iteration line.',
  '',
  '## Non-goals',
  '- A change outside the working directory.',
  '',
  '## Assumed scope',
  'native-goal-progress.txt',
  '',
].join('\n')

/**
 * Answer the model calls of Grok's own goal machinery (Grok Build 1.0.46).
 *
 * - `/goal` starts a Goal Plan Writer subagent first. It must write its plan to
 *   the file that its prompt states, then answer exactly `Done`. Without a plan
 *   Grok pauses the goal: "Goal paused. No plan was produced."
 * - After a round, a hidden completion evaluator answers in a JSON schema.
 *   Text that is no verdict fails the evaluation twice, and Grok then pauses
 *   the goal ("Goal evaluation failed after a bounded retry"). That is the
 *   automatic pause after the resumed round.
 */
function grokGoalMachineryRules({ marker }: NativeGoalScenario): MockModelRule[] {
  return [{
    name: `grok-goal-plan-write-${marker}`,
    priority: 'high',
    when: { body: ['You are the Goal Plan Writer', marker], lastMessage: { role: 'user' } },
    once: true,
    respond: {
      captures: { planPath: 'your only write is `([^`]+)`' },
      toolCalls: [writeToolCall(AgentProvider.GROK_BUILD, 'grok-goal-plan-write', { path: '{{planPath}}', content: GOAL_PLAN })],
    },
  }, {
    name: `grok-goal-plan-done-${marker}`,
    priority: 'high',
    when: { body: ['You are the Goal Plan Writer', marker], lastMessage: { role: 'tool' } },
    once: true,
    respond: { text: 'Done' },
  }, {
    name: `grok-goal-evaluator-${marker}`,
    priority: 'high',
    when: { system: 'hidden completion evaluator', body: marker },
    respond: { text: 'The evaluation states no verdict.' },
  }]
}

// Grok Build runs the whole goal loop inside one turn, queues a prompt that
// arrives during it, and yields to that prompt after the running round. So the
// `/goal pause` command goes to Grok's own queue at once, and the held round
// finishes before the pause.
grokTest('records the native user pause and resumes new native goal model work', async ({ native }) => {
  const { page, leapmuxServer } = native
  await exerciseNativeGoalPauseAndResume(native, {
    pauseTiming: 'after-the-round',
    supportRules: grokGoalMachineryRules,
    pausedProof: async () => {
      // Grok answers a pause of an active goal so. A pause that reached a goal
      // that already paused itself reads "Goal is already paused." instead.
      await expect(messageBubbles(page).filter({ hasText: 'Goal paused. Use /goal resume to continue.' }).first()).toBeVisible()
      const agent = await currentNativeAgent(native)
      const channel = await getTestChannel(leapmuxServer.hubUrl, leapmuxServer.adminToken)
      // `user_paused` states no reason. Each pause of Grok's own states one.
      await expect.poll(async () => {
        const response = await channel.callWorker(leapmuxServer.workerId, 'ListAgentMessages', ListAgentMessagesRequestSchema, ListAgentMessagesResponseSchema, { agentId: agent.id, limit: 1 })
        return { status: response.goal?.status, statusDetail: response.goal?.statusDetail }
      }).toEqual({ status: AgentGoalStatus.PAUSED, statusDetail: '' })
    },
  })
})
