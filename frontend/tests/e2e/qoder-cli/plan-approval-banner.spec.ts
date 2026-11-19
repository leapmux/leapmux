import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { enterPlanModeToolCall, exitPlanModeFromFileToolCall, writeToolCall } from '../helpers/providerToolCalls'
import { answerControl, answerPlanReview, enterControlFeedback, expectNoControlBanner, sendMessage, waitForAgentIdle, waitForControlBanner, waitForSettingsHydrated } from '../helpers/ui'
import { qoderTest } from '../qoder-fixtures'
import { expectQoderModeChip } from './scenarios'

qoderTest.describe('Qoder CLI plan approval', () => {
  const PROVIDER = AgentProvider.QODER

  function planText(revision: string): string {
    return `# Dummy plan ${revision}\n\nNever execute this plan.`
  }

  const PLAN_FILE_CAPTURE = { planFile: '(?:create your plan at|already exists at) (\\S+?\\.md)' }

  function planSteps(revision: string) {
    return {
      write: {
        toolCalls: [
          writeToolCall(PROVIDER, `write-plan-${revision}`, {
            path: '{{planFile}}',
            content: planText(revision),
          }),
        ],
        captures: PLAN_FILE_CAPTURE,
      },
      review: {
        toolCalls: [
          exitPlanModeFromFileToolCall(PROVIDER, `exit-plan-${revision}`, []),
        ],
      },
    }
  }

  qoderTest('reviews the plan, retains it on reject, and applies the selected mode on approve', async ({ authenticatedQoderWorkspace, page, modelScript }) => {
    void authenticatedQoderWorkspace
    await waitForSettingsHydrated(page)
    // Plan mode can start additional model turns.
    // Each plan decision can start another turn.
    await modelScript.fallback({ text: 'Working through the plan.' })

    const enter = await modelScript.queue({
      toolCalls: [
        enterPlanModeToolCall(PROVIDER, 'enter-plan'),
      ],
    })
    await sendMessage(page, modelScript.prompt('Enter plan mode and present the plan.'))
    await modelScript.waitForSteps(enter + 1)
    const enterBanner = await waitForControlBanner(page)
    await expect(enterBanner).toContainText('EnterPlanMode')
    await answerControl(page, 'allow')
    await waitForAgentIdle(page)
    await expectQoderModeChip(page, 'Plan')

    const firstSteps = planSteps('first')
    await modelScript.rule({
      name: 'write the first requested plan',
      when: {
        user: 'Write the plan file, then present it for review\\.',
        lastMessage: { role: 'user' },
      },
      respond: firstSteps.write,
      once: true,
    })
    await modelScript.rule({
      name: 'review the first written plan',
      when: {
        lastMessage: { role: 'tool', text: 'File created successfully at:' },
      },
      respond: firstSteps.review,
      once: true,
    })
    await sendMessage(page, modelScript.prompt('Write the plan file, then present it for review.'))

    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText('Plan Ready for Review')
    await expectQoderModeChip(page, 'Plan')

    // A typed reply rejects the plan with feedback, and the session stays in
    // plan mode.
    await enterControlFeedback(page, 'not ready yet')
    await answerPlanReview(page, 'reject')
    await expectNoControlBanner(page)
    await waitForAgentIdle(page)
    await expectQoderModeChip(page, 'Plan')

    const secondSteps = planSteps('second')
    await modelScript.rule({
      name: 'write the requested plan revision',
      when: {
        user: 'Revise the plan file and present it again\\.',
        lastMessage: { role: 'user' },
      },
      respond: secondSteps.write,
      once: true,
    })
    await modelScript.rule({
      name: 'review the written plan revision',
      when: {
        lastMessage: { role: 'tool', text: '(?:File (?:created|updated) successfully at:|has been updated)' },
      },
      respond: secondSteps.review,
      once: true,
    })
    await sendMessage(page, modelScript.prompt('Revise the plan file and present it again.'))
    const banner2 = await waitForControlBanner(page)
    await expect(banner2).toContainText('Plan Ready for Review')
    await answerPlanReview(page, 'approve')
    await expectNoControlBanner(page)
    await waitForAgentIdle(page)
    // The review control selects Auto. The next turn keeps the approved plan.
    await expectQoderModeChip(page, 'Auto')
    const next = await modelScript.queue({ text: 'The approved mode stayed active.' })
    await sendMessage(page, modelScript.prompt('Reply after the approved plan.'))
    await modelScript.waitForSteps(next + 1)
    await waitForAgentIdle(page)
    // The read fails when the agent never requested the step.
    const nextBody = JSON.stringify((await modelScript.requestAt(next)).body)
    expect(nextBody.includes('# Dummy plan second')).toBe(true)
    expect(nextBody.includes('Exited Plan Mode')).toBe(true)
  })
})
