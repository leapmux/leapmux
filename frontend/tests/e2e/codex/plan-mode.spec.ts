import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codexTest } from '../codex-fixtures'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { askUserQuestionToolCall } from '../helpers/providerToolCalls'
import { chooseSettingsOption, controlBanner, controlButton, sendMessage, waitForAgentIdle, waitForSettingsIdle } from '../helpers/ui'

codexTest('enables a native planning question and rejects the same tool in Default mode', async ({ authenticatedCodexWorkspace, page, modelScript }) => {
  void authenticatedCodexWorkspace
  await chooseSettingsOption(page, 'collaboration_mode-plan')
  await waitForSettingsIdle(page)
  const question = [{ header: 'Choice', question: 'Which plan should I use?', options: [{ label: 'Blue', description: 'Use the blue plan.' }, { label: 'Red', description: 'Use the red plan.' }] }]
  const asked = await modelScript.queue({ toolCalls: [askUserQuestionToolCall(AgentProvider.CODEX, 'native-plan-question', question)] }, { text: 'The native plan question ended.' })
  await sendMessage(page, modelScript.prompt('Ask for this plan choice.'))
  await modelScript.waitForSteps(asked + 1)
  const banner = controlBanner(page)
  await expect(banner).toContainText('Which plan should I use?')
  await banner.getByTestId('question-option-Red').click()
  await controlButton(page, 'submit').click()
  expect(nativeToolResult(await modelScript.requestAt(asked + 1), 'native-plan-question')).toContain('Red')
  await waitForAgentIdle(page)
  await chooseSettingsOption(page, 'collaboration_mode-default')
  await waitForSettingsIdle(page)
  const refused = await modelScript.queue({ toolCalls: [askUserQuestionToolCall(AgentProvider.CODEX, 'default-plan-question', question)] }, { text: 'The Default mode refusal ended.' })
  await sendMessage(page, modelScript.prompt('Attempt the same native question after leaving Plan mode.'))
  expect(nativeToolResult(await modelScript.requestAt(refused + 1), 'default-plan-question')).toMatch(/unavailable|plan mode|not supported/i)
  await waitForAgentIdle(page)
  await expect(banner).toHaveCount(0)
})
