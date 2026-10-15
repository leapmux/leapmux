import { expect } from '@playwright/test'
import { acpExtractControl } from '../../../src/components/chat/providers/acp/extractControl'
import { watchNativeControls } from '../helpers/nativeControlWatch'
import { exerciseQuestionAnswer, exerciseQuestionCancellation } from '../helpers/nativeQuestion'
import { exerciseQuestionIsolation } from '../helpers/nativeQuestionIsolation'
import { currentNativeAgent, nativeModelToolNames } from '../helpers/nativeScenario'
import { readObservedNativeDecision, waitForOneNativeControl } from '../helpers/nativeStoredControlDecision'
import { chooseSettingsOption, controlActions, controlButton, waitForControlBanner, waitForSettingsIdle } from '../helpers/ui'
import { exerciseUnsupportedControlThroughPermission } from '../helpers/unsupportedNativeControl'
import { reasonixTest } from '../reasonix-fixtures'

reasonixTest('offers an actual native permission without an agent question sequence', async ({ native }) => {
  await chooseSettingsOption(native.page, 'tool_approval-ask')
  await waitForSettingsIdle(native.page)
  await exerciseUnsupportedControlThroughPermission(native, {
    purpose: 'question',
    classify: acpExtractControl,
    // These aliases are absent. The native `ask` round trip below checks Reasonix's actual question route.
    nativeProof: (request) => {
      const tools = nativeModelToolNames(request)
      expect(tools.length).toBeGreaterThan(0)
      expect(tools.some(tool => /^(?:ask_user|ask_user_question|AskUserQuestion|__human_input)$/.test(tool))).toBe(false)
    },
  })
})

reasonixTest('returns the selected answer through its native ask permission after reload', async ({ native }) => {
  const { page } = native
  const { result } = await exerciseQuestionAnswer(native, {
    questions: [{
      header: 'Database',
      question: 'Which database should this native test use?',
      options: [{ label: 'Postgres', description: 'Use Postgres.' }, { label: 'SQLite', description: 'Use SQLite.' }],
    }],
    callId: 'reasonix-native-ask',
    reply: async () => {
      await page.reload()
      await expect(await waitForControlBanner(page)).toContainText('Which database should this native test use?')
      await controlActions(page).getByTestId('control-more-actions').click()
      await page.getByTestId('control-decision-q1:2').filter({ visible: true }).click()
    },
  })
  expect(result).toContain('Database: SQLite')
  expect(result).not.toContain('Postgres')
})

reasonixTest('cancels the native ask request without starting another model turn', async ({ native }) => {
  const agent = await currentNativeAgent(native)
  const watch = await watchNativeControls(native.leapmuxServer, agent.id)
  try {
    await exerciseQuestionCancellation(native, {
      questions: [{
        header: 'Database',
        question: 'Which database should this native test use?',
        options: [{ label: 'Postgres', description: 'Use Postgres.' }, { label: 'SQLite', description: 'Use SQLite.' }],
      }],
      callId: 'reasonix-cancelled-ask',
      reply: async () => {
        await native.page.reload()
        await expect(await waitForControlBanner(native.page)).toContainText('Which database should this native test use?')
        await controlButton(native.page, 'deny').click()
      },
    })
    const observed = await waitForOneNativeControl(watch)
    const { decision } = await readObservedNativeDecision(native, agent, watch, observed)
    expect(decision.response).toMatchObject({ result: { outcome: { outcome: 'selected', optionId: 'q1:cancel' } } })
  }
  finally {
    watch.cancel()
  }
})

reasonixTest('returns one native answer for each sequential question', async ({ native }) => {
  const questions = [
    { header: 'Database', question: 'Which native database?', options: [{ label: 'Postgres', description: 'Use Postgres.' }, { label: 'SQLite', description: 'Use SQLite.' }] },
    { header: 'Size', question: 'Which native size?', options: [{ label: 'Small', description: 'Use small.' }, { label: 'Large', description: 'Use large.' }] },
  ]
  const { result } = await exerciseQuestionAnswer(native, {
    questions,
    reply: async () => {
      for (const [index, question] of questions.entries()) {
        await expect(await waitForControlBanner(native.page)).toContainText(question.question)
        await native.page.reload()
        await expect(await waitForControlBanner(native.page)).toContainText(question.question)
        await controlActions(native.page).getByTestId('control-more-actions').click()
        await native.page.getByTestId(`control-decision-q${index + 1}:2`).filter({ visible: true }).click()
      }
    },
  })
  expect(result).toContain('Database: SQLite')
  expect(result).toContain('Size: Large')
  expect(result).not.toContain('Postgres')
  expect(result).not.toContain('Small')
})

reasonixTest('keeps a native question on its owner tab and workspace after reload', async ({ native }) => {
  await exerciseQuestionIsolation(native, { customAnswerDraft: false })
})
