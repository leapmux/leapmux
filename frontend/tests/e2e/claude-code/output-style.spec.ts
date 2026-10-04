import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '../../../src/lib/jsonPick'
import { claudeTest } from '../claude-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { chooseSettingsOption, expectSettingsOptionChosen, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'

claudeTest('applies native output-style instructions and restores the selected style', async ({ authenticatedClaudeWorkspace, page, modelScript }) => {
  void authenticatedClaudeWorkspace
  const context = { page, modelScript, provider: AgentProvider.CLAUDE_CODE }
  await waitForSettingsHydrated(page)
  let initialModel: unknown
  let initialEffort: unknown
  for (const [index, choice] of [
    { style: 'default', reload: false, marker: null },
    { style: 'Explanatory', reload: false, marker: '# Explanatory Style Active' },
    { style: 'Explanatory', reload: true, marker: '# Explanatory Style Active' },
    { style: 'Learning', reload: false, marker: '# Learning Style Active' },
    { style: 'default', reload: false, marker: 'The output style was reset to the default.' },
  ].entries()) {
    if (choice.reload) {
      await page.reload()
      await waitForSettingsHydrated(page)
    }
    else {
      await chooseSettingsOption(page, `outputStyle-${choice.style}`)
      await waitForSettingsIdle(page)
    }
    await expectSettingsOptionChosen(page, `outputStyle-${choice.style}`)
    const request = await sendNativeAnswer(context, `Complete output style turn ${index}.`, `OUTPUT_STYLE_ANSWER_${index}`)
    if (!isObject(request.body) || !Array.isArray(request.body.messages))
      throw new Error('The native output-style request has no messages.')
    const messages = request.body.messages.filter(isObject)
    const lastUser = messages.findLastIndex(message => message.role === 'user')
    expect(lastUser).toBeGreaterThanOrEqual(0)
    const styleMessages = messages.filter(message => message.role === 'system' && /Style Active|output style was reset/.test(JSON.stringify(message)))
    const instructions = choice.reload
      ? JSON.stringify(styleMessages.at(-1)) ?? ''
      : JSON.stringify(messages.slice(lastUser + 1).filter(message => message.role === 'system'))
    if (choice.marker)
      expect(instructions).toContain(choice.marker)
    else
      expect(instructions).not.toContain('Style Active')
    const effort = isObject(request.body.output_config) ? request.body.output_config.effort : undefined
    if (index === 0) {
      expect(request.protocol).toBe('anthropic-messages')
      expect(request.body.model).toEqual(expect.stringMatching(/^claude-/))
      initialModel = request.body.model
      initialEffort = effort
    }
    expect(request.body.model).toBe(initialModel)
    expect(effort).toBe(initialEffort)
  }
})
