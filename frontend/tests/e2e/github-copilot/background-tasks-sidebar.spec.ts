import { expect } from '@playwright/test'
import { copilotTest } from '../copilot-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { currentNativeAgent, nativeTextStep } from '../helpers/nativeScenario'
import { copilotAsyncBashToolCall } from '../helpers/providerToolCalls'
import { createToolOutputControl } from '../helpers/toolOutputControl'
import { assistantBubbles, messageContents, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { exerciseCopilotChildTranscript } from './childScenario'
import { bypassToolRequests } from './scenarios'

copilotTest('background-tasks-sidebar: routes the prompt, response, and completion into the child tab', async ({ native }) => {
  await exerciseCopilotChildTranscript(native)
})

copilotTest('shows a completion notice when an asynchronous shell exits', async ({ native }) => {
  const { page, modelScript } = native
  await bypassToolRequests(native)
  const agent = await currentNativeAgent(native)
  const output = createToolOutputControl(agent.workingDir)
  const notice = 'Shell Run the scripted background command exited (code 0)'
  await modelScript.fallback(nativeTextStep(native, 'The background shell completion was received.'))
  await withCleanup(async () => {
    const start = await modelScript.queue(
      {
        toolCalls: [
          copilotAsyncBashToolCall('background-shell', output.command),
        ],
      },
      nativeTextStep(native, 'The background command is active.'),
    )
    await sendMessage(page, modelScript.prompt('Start the scripted asynchronous command.'))
    await output.waitForFirstOutput()
    await modelScript.waitForSteps(start + 2)
    // The command remains active after this answer. Release it before requiring session idle.
    await expect(assistantBubbles(page).filter({ hasText: 'The background command is active.' })).toHaveCount(1)
    await expect(messageContents(page).filter({ hasText: notice })).toHaveCount(0)
    await output.releaseFirstOutput()
    await output.waitForSecondOutput()
    await output.releaseFinalOutput()
    await expect(messageContents(page).filter({ hasText: notice })).toHaveCount(1)
    await waitForAgentIdle(page)
    await page.reload()
    await waitForAgentIdle(page)
    await expect(messageContents(page).filter({ hasText: notice })).toHaveCount(1)
  }, async () => {
    await output.releaseFirstOutput()
    await output.releaseFinalOutput()
  })
})
