import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '../../../src/lib/jsonPick'
import { claudeTest } from '../claude-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { chooseSettingsOption, expectSettingsOptionChosen, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'

claudeTest('applies native fast speed and clears it across reloads with a fixed Opus model', async ({ authenticatedClaudeWorkspace, page, modelScript }) => {
  void authenticatedClaudeWorkspace
  const context = { page, modelScript, provider: AgentProvider.CLAUDE_CODE }
  await waitForSettingsHydrated(page)
  await chooseSettingsOption(page, 'model-opus')
  await waitForSettingsIdle(page)
  let model: unknown
  for (const [index, { fast, reload }] of [{ fast: false, reload: false }, { fast: true, reload: false }, { fast: true, reload: true }, { fast: false, reload: false }, { fast: false, reload: true }].entries()) {
    if (reload) {
      await page.reload()
      await waitForSettingsHydrated(page)
    }
    else {
      await chooseSettingsOption(page, `fastMode-${fast ? 'on' : 'off'}`)
      await waitForSettingsIdle(page)
    }
    await expectSettingsOptionChosen(page, `fastMode-${fast ? 'on' : 'off'}`)
    const request = await sendNativeAnswer(context, `Run fast speed turn ${index}.`, `FAST_SPEED_ANSWER_${index}`)
    expect(request.protocol).toBe('anthropic-messages')
    if (!isObject(request.body))
      throw new Error('The native fast speed request has no object body.')
    if (index === 0) {
      expect(request.body.model).toEqual(expect.stringMatching(/^claude-opus-/))
      model = request.body.model
    }
    expect(request.body.model).toBe(model)
    if (fast)
      expect(request.body.speed).toBe('fast')
    else
      expect(request.body).not.toHaveProperty('speed')
  }
})
