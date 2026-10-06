import { existsSync } from 'node:fs'

import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { clineTest, offeredTools } from '../cline-fixtures'
import { exitPlanModeToolCall } from '../helpers/providerToolCalls'
import { answerPlanReview, assistantBubbles, controlBanner, enterControlFeedback, expectSettingsChip, sendMessage, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'

/**
 * The native plan exit opens the plan review controls. The approval must change the native mode and continue the plan.
 *
 * The Worker starts one private Cline hub for this agent. Cline's DeepSeek provider sends requests to the isolated mock.
 *
 * The switch_to_act_mode tool opens the actual plan review. Approval recreates the same native session in Act and continues the plan.
 */
const PROVIDER = AgentProvider.CLINE

clineTest.describe('Cline control requests', () => {
  clineTest('approves the plan, switches to Act, and continues the plan', async ({ planningClineWorkspace, page, modelScript }) => {
    const note = join(planningClineWorkspace.workingDir, 'note.txt')
    await expectSettingsChip(page, 'Plan')

    // Cline's plan mode presents the plan in an answer and waits for the reader's
    // reply. The model calls the plan tool only after that reply.
    const planned = await modelScript.queue({ text: 'Plan:\n1. Create note.txt.\n2. Verify it.' })
    await sendMessage(page, modelScript.prompt('Plan how to write the note.'))
    await modelScript.waitForSteps(planned + 1)
    await waitForAgentIdle(page)

    const start = await modelScript.queue(
      { toolCalls: [exitPlanModeToolCall(PROVIDER, 'cline-plan', '')] },
      // The continuation prompt that the worker sends after the switch, in Act mode.
      { text: 'Starting the approved plan.' },
    )
    await sendMessage(page, modelScript.prompt('Looks good, go ahead.'))
    await modelScript.waitForSteps(start + 1)
    await waitForControlBanner(page)
    await answerPlanReview(page, 'approve')
    await expect(controlBanner(page)).toHaveCount(0)

    await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)
    await expectSettingsChip(page, 'Act')
    await expect(assistantBubbles(page).filter({ hasText: 'Starting the approved plan.' })).toBeVisible()

    // The plan turn offered the plan tool and no editor. The new Act session offers
    // the editor, holds the conversation, and reads the worker's continuation prompt.
    const planTurn = (await modelScript.requestAt(start)).body
    expect(offeredTools(planTurn)).toContain('switch_to_act_mode')
    expect(offeredTools(planTurn)).not.toContain('editor')
    const actTurn = (await modelScript.requestAt(start + 1)).body
    expect(offeredTools(actTurn)).toContain('editor')
    expect(offeredTools(actTurn)).not.toContain('switch_to_act_mode')
    expect(JSON.stringify(actTurn)).toContain('Create note.txt.')
    expect(JSON.stringify(actTurn)).toContain('The user approved switching to act mode.')
    expect(existsSync(note)).toBe(false)
  })

  clineTest('keeps Plan mode when the reader rejects the plan with feedback', async ({ planningClineWorkspace, page, modelScript }) => {
    void planningClineWorkspace
    const start = await modelScript.queue(
      { toolCalls: [exitPlanModeToolCall(PROVIDER, 'cline-plan-rejected', '')] },
      { text: 'I will split the plan.' },
    )
    await sendMessage(page, modelScript.prompt('The plan is fine, switch to act mode.'))
    await modelScript.waitForSteps(start + 1)
    await waitForControlBanner(page)
    await enterControlFeedback(page, 'Split the plan into two steps first.')
    await answerPlanReview(page, 'reject')
    await expect(controlBanner(page)).toHaveCount(0)

    await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)
    await expectSettingsChip(page, 'Plan')
    // The refusal reached the model as the plan tool's error, and the session still
    // offers the plan tool.
    const followUp = (await modelScript.requestAt(start + 1)).body
    expect(JSON.stringify(followUp)).toContain('Split the plan into two steps first.')
    expect(offeredTools(followUp)).toContain('switch_to_act_mode')
    expect(offeredTools(followUp)).not.toContain('editor')
  })
})
