import { expect } from '@playwright/test'
import { clineTest } from '../cline-fixtures'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, chooseSettingsOption, expectAssistantAnswer, expectSettingsChip, SECOND_ARITHMETIC_ANSWER, SECOND_ARITHMETIC_ANSWER_TEXT, SECOND_ARITHMETIC_PROMPT, sendMessage, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { offeredTools } from './offeredTools'

/**
 * The mode choice must reach the actual native session. The browser must follow native changes and refusal limits.
 *
 * The Worker starts one private Cline hub for this agent. Cline's DeepSeek provider sends requests to the isolated mock.
 *
 * Cline fixes tools and system instructions when it creates a session. A Plan or Act change recreates the same session with its earlier messages.
 */
clineTest.describe('Cline settings', () => {
  clineTest('moves the session between Act and Plan, and keeps the conversation', async ({ askingClineWorkspace, page, modelScript }) => {
    void askingClineWorkspace
    await waitForSettingsHydrated(page)
    const actStep = await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps(actStep + 1)
    await waitForAgentIdle(page)
    await expectAssistantAnswer(page)

    await chooseSettingsOption(page, 'permissionMode-plan')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Plan')

    const planStep = await modelScript.queue({ text: SECOND_ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(SECOND_ARITHMETIC_PROMPT))
    await modelScript.waitForSteps(planStep + 1)
    await waitForAgentIdle(page)
    await expectAssistantAnswer(page, { answer: SECOND_ARITHMETIC_ANSWER })

    await chooseSettingsOption(page, 'permissionMode-act')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Act')
    await page.reload()
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Act')

    const actAgainStep = await modelScript.queue({ text: 'Back in Act.' })
    await sendMessage(page, modelScript.prompt('Reply with the words: Back in Act.'))
    await modelScript.waitForSteps(actAgainStep + 1)
    await waitForAgentIdle(page)

    const [act, plan, actAgain] = await Promise.all([actStep, planStep, actAgainStep].map(async step => (await modelScript.requestAt(step)).body))
    // Act offers the editor. Plan offers the plan tool in its place.
    expect(offeredTools(act)).toContain('editor')
    expect(offeredTools(act)).not.toContain('switch_to_act_mode')
    expect(offeredTools(plan)).toContain('switch_to_act_mode')
    expect(offeredTools(plan)).not.toContain('editor')
    expect(offeredTools(actAgain)).toContain('editor')
    expect(offeredTools(actAgain)).not.toContain('switch_to_act_mode')
    // Each new session holds the conversation of the session before it.
    expect(JSON.stringify(plan)).toContain(ARITHMETIC_ANSWER_TEXT)
    expect(JSON.stringify(actAgain)).toContain(ARITHMETIC_ANSWER_TEXT)
    expect(JSON.stringify(actAgain)).toContain(SECOND_ARITHMETIC_ANSWER_TEXT)
  })
})
