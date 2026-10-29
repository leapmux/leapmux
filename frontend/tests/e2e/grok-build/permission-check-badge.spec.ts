import { join } from 'node:path'
import { expect } from '@playwright/test'
import { grokTest } from '../grok-fixtures'
import { expectNoUnrenderedRow } from '../helpers/nativeConversation'
import { currentNativeAgent, nativeTextStep } from '../helpers/nativeScenario'
import { bashToolCall } from '../helpers/providerToolCalls'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { applyPermissionPreset, assistantBubbles, sendMessage, toolRunningBadge, waitForAgentIdle } from '../helpers/ui'

/**
 * The permission-check badge while the Smart classifier holds a tool call.
 *
 * Auto mode (the Smart preset) reviews every risky command through its own model-backed
 * permission check before the call runs, and `pending_interaction` is the only live signal
 * that the call still waits there: it becomes the running row's badge until the review
 * answers. A gate on the review's model request holds the badge on screen.
 */
grokTest('shows the permission-check badge while the Smart classifier holds a tool call', async ({ native }) => {
  const { page, modelScript } = native
  const agent = await currentNativeAgent(native)
  const file = join(agent.workingDir, 'smart-permission-badge.txt')
  await applyPermissionPreset(page, 'smart')
  const gate = 'held-smart-review'
  await modelScript.rule({
    name: 'held-smart-review',
    when: { system: '^You review a command that a coding agent wants to run' },
    respond: { text: '{"thinking":"The command removes the fixture file.","shouldBlock":true,"reason":"Ask before the fixture removal."}', gate },
  })
  const start = await modelScript.queue(
    {
      toolCalls: [
        bashToolCall(native.provider, 'smart-permission-badge', `rm -rf ${quotePosixShellArgument(file)}`),
      ],
    },
    nativeTextStep(native, 'The Smart reviewer blocked the removal.'),
  )
  try {
    await sendMessage(page, modelScript.prompt('Attempt the scripted risky removal under Smart permissions.'))
    await modelScript.waitForGate(gate)
    const badge = toolRunningBadge(page)
    await expect(badge).toHaveText('Checking permission…')
    await modelScript.releaseGate(gate)
    // The review's answer ends the check, so the badge leaves with it.
    await expect(badge).not.toBeVisible()
    await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)
    await expect(assistantBubbles(page).filter({ hasText: 'The Smart reviewer blocked the removal.' })).not.toHaveCount(0)
    await expectNoUnrenderedRow(page)
  }
  finally {
    await modelScript.releaseGateIfHeld(gate)
  }
})
