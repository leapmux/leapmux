import { CODEX_OPTION, CODEX_OPTION_DEFAULT } from '../../../src/generated/contracts/codex-protocol'
import { isObject } from '../../../src/lib/jsonPick'
import { codexTest, expect } from '../codex-fixtures'
import { assistantBubbles, chooseSettingsOption, expectSettingsOptionChosen, sendMessage, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'

codexTest('applies Fast to native turns and clears it when Default returns', async ({ authenticatedCodexWorkspace, page, modelScript }) => {
  void authenticatedCodexWorkspace
  const fastOption = `${CODEX_OPTION.ServiceTier}-fast`
  const defaultOption = `${CODEX_OPTION.ServiceTier}-${CODEX_OPTION_DEFAULT.ServiceTier}`
  await waitForSettingsHydrated(page)
  await expectSettingsOptionChosen(page, defaultOption)

  for (const [stepIndex, settings] of [
    { fast: false, reload: false },
    { fast: true, reload: false },
    { fast: true, reload: true },
    { fast: false, reload: false },
    { fast: false, reload: true },
  ].entries()) {
    const option = settings.fast ? fastOption : defaultOption
    if (stepIndex === 1 || stepIndex === 3) {
      await chooseSettingsOption(page, option)
      await waitForSettingsIdle(page)
    }
    if (settings.reload) {
      await page.reload()
      await waitForSettingsHydrated(page)
    }
    await expectSettingsOptionChosen(page, option)

    const answer = `The native tier turn ${stepIndex} ended.`
    await modelScript.queue({ text: answer })
    await sendMessage(page, modelScript.prompt(`Reply once for native tier turn ${stepIndex}.`))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const request = status.requests.find(record => record.stepIndex === stepIndex)
    expect(request?.protocol).toBe('openai-responses')
    if (!isObject(request?.body))
      throw new Error('The native Codex tier request has no object body.')
    if (settings.fast)
      expect(request.body.service_tier).toBe('priority')
    else
      expect(request.body).not.toHaveProperty('service_tier')
    await expect(assistantBubbles(page).filter({ hasText: answer }).first()).toBeVisible()
  }
})
