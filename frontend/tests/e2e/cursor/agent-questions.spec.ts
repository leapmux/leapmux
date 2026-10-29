import type { QuestionRequest } from '../helpers/providerToolCalls'
import { expect } from '@playwright/test'
import { accountStorageKey, PREFIX_CONTROL_STATE } from '../../../src/lib/browserStorage'
import { isObject } from '../../../src/lib/jsonPick'
import { cursorTest } from '../cursor-fixtures'
import { getUserId } from '../helpers/api'
import { chooseQuestionPages } from '../helpers/nativeQuestion'
import { exerciseQuestionIsolation } from '../helpers/nativeQuestionIsolation'
import { restoreQuestionSelection } from '../helpers/nativeQuestionRestoration'
import { askUserQuestionToolCall } from '../helpers/providerToolCalls'
import { readEntry, storageKeys, writeEntry } from '../helpers/storage'
import { assistantBubbles, composerEditor, controlAgentId, controlButton, sendMessage, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'

// Cursor's service answers the question itself in the same Run exchange, so the turn is one scripted step and the
// answer is the service's own words for the chosen option.
cursorTest('sends the selected question answer to the native Run stream', async ({ native }) => {
  const { page, modelScript } = native
  const start = await modelScript.queue({
    toolCalls: [
      askUserQuestionToolCall(native.provider, 'cursor-color', [
        {
          header: 'Color',
          question: 'Which color should I use?',
          options: [
            { label: 'Blue', description: 'Use blue.' },
            { label: 'Green', description: 'Use green.' },
          ],
        },
      ]),
    ],
  })
  await sendMessage(page, modelScript.prompt('Ask which color to use.'))
  await modelScript.waitForSteps(start + 1)
  const banner = await waitForControlBanner(page)
  await expect(banner).toContainText('Which color should I use?')
  await banner.getByTestId('question-option-Blue').click()
  await restoreQuestionSelection(native, banner, 'Blue')
  await controlButton(page, 'submit').click()

  await waitForAgentIdle(page)
  await expect(assistantBubbles(page).filter({ hasText: 'Cursor question selected: option-1-1' }).first()).toBeVisible()
  await page.reload()
  await expect(assistantBubbles(page).filter({ hasText: 'Cursor question selected: option-1-1' }).first()).toBeVisible()
})

for (const singleQuestion of [false, true]) {
  cursorTest(singleQuestion ? 'keeps custom answers unavailable on the native ACP path' : 'returns multiple native answers after their pages survive reload', async ({ native }) => {
    const questions: QuestionRequest[] = [
      {
        header: 'Color',
        question: 'Choose a native color.',
        options: [
          { label: 'Blue', description: 'Use blue.' },
          { label: 'Green', description: 'Use green.' },
        ],
      },
      ...singleQuestion
        ? []
        : [
            {
              header: 'Size',
              question: 'Choose native sizes.',
              multiSelect: true,
              options: [
                { label: 'Small', description: 'Use small.' },
                { label: 'Medium', description: 'Use medium.' },
                { label: 'Large', description: 'Use large.' },
              ],
            },
          ],
    ]
    const start = await native.modelScript.queue({
      toolCalls: [
        askUserQuestionToolCall(native.provider, 'cursor-pages', questions),
      ],
    })
    await sendMessage(native.page, native.modelScript.prompt('Ask the native questions.'))
    await native.modelScript.waitForSteps(start + 1)
    const banner = await waitForControlBanner(native.page)
    if (singleQuestion) {
      await expect(banner).toContainText('This agent accepts selected options only.')
      await expect(composerEditor(native.page)).toHaveCount(0)
      await expect(controlButton(native.page, 'submit')).toBeDisabled()
    }
    await chooseQuestionPages(native, questions, singleQuestion
      ? [
          {
            labels: ['Green'],
          },
        ]
      : [
          {
            labels: ['Green'],
          },
          {
            labels: ['Large', 'Small'],
          },
        ])(banner)
    await waitForAgentIdle(native.page)
    const expected = singleQuestion ? 'Cursor question selected: option-1-2' : 'Cursor question selected: option-1-2, option-2-3, option-2-1'
    const answer = assistantBubbles(native.page).filter({ hasText: expected }).first()
    await expect(answer).toBeVisible()
    await native.page.reload()
    await expect(answer).toBeVisible()
    await expect(answer).not.toContainText('option-1-1')
    await expect(answer).not.toContainText('option-2-2')
  })
}

cursorTest('keeps a native question on its owner tab and workspace after reload', async ({ native }) => {
  await exerciseQuestionIsolation(native, { customAnswerDraft: false })
})

cursorTest('repairs invalid saved navigation and hidden text before answering a selected-only question', async ({ native }) => {
  const { page, modelScript, leapmuxServer: server } = native
  await modelScript.queue({
    toolCalls: [
      askUserQuestionToolCall(native.provider, 'cursor-restored-state', [
        {
          header: 'Color',
          question: 'Confirm the native color.',
          options: [
            { label: 'Green', description: 'Use green.' },
          ],
        },
      ]),
    ],
  })
  await sendMessage(page, modelScript.prompt('Ask for the native color confirmation.'))
  await modelScript.waitForSteps()
  const banner = await waitForControlBanner(page)
  await banner.getByTestId('question-option-Green').click()
  await restoreQuestionSelection(native, banner, 'Green')
  const agentId = await controlAgentId(await waitForControlBanner(page))
  const userId = server.adminUserId ?? await getUserId(server.hubUrl, server.adminToken)
  const prefix = accountStorageKey(userId, `${PREFIX_CONTROL_STATE}${agentId}:`)
  const keys = (await storageKeys(page)).filter(key => key.startsWith(prefix))
  expect(keys).toHaveLength(1)
  const key = keys[0]!
  const stored = await readEntry(page, key)
  if (!stored || !isObject(stored.v))
    throw new Error('The selected native answer must reach durable storage.')
  await writeEntry(page, key, {
    ...stored.v,
    currentPage: 999,
    customTexts: { 0: 'A hidden old draft.' },
  }, stored.e)
  await page.reload()
  const restored = await waitForControlBanner(page)
  await expect(restored).toContainText('Confirm the native color.')
  await expect(restored.getByTestId('question-option-Green').locator('input')).toBeChecked()
  await expect(composerEditor(page)).toHaveCount(0)
  await controlButton(page, 'submit').click()
  await waitForAgentIdle(page)
  await expect(assistantBubbles(page).filter({ hasText: 'Cursor question selected: option-1-1' }).first()).toBeVisible()
})
