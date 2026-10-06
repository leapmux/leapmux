import type { NativeControlFrame } from '../helpers/nativeControlWatch'
import { diracTest, expect } from '../dirac-fixtures'
import { watchNativeControls } from '../helpers/nativeControlWatch'
import { selectedAgentTabId } from '../helpers/nativeScenario'
import { controlButton, waitForSettingsHydrated } from '../helpers/ui'
import { diracQuestionControl } from './questionControl'
import { DIRAC_COLOR_QUESTION, exerciseQuestionReply } from './questionScenarios'
import { nativeContext } from './scenarios'

/** The controls that a watch saw, as the body of an attachment. A failed watch gives its failure instead. */
function controlsReport(watch: { controls: () => readonly NativeControlFrame[] }): string {
  try {
    return JSON.stringify(watch.controls(), null, 2)
  }
  catch (error) {
    return JSON.stringify({ watchFailure: error instanceof Error ? error.message : String(error) }, null, 2)
  }
}

diracTest.describe('dirac agent questions', () => {
  diracTest('returns the selected form answer through the native question tool', async ({ askingDiracWorkspace, page, leapmuxServer, modelScript }, testInfo) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingDiracWorkspace.workspaceId })
    await waitForSettingsHydrated(page)
    const agentId = await selectedAgentTabId(page)
    const watch = await watchNativeControls(leapmuxServer, agentId)
    try {
      const answer = await exerciseQuestionReply(context, async (form) => {
        await testInfo.attach('dirac-question-form', { body: await form.evaluate(element => element.outerHTML), contentType: 'text/html' })
        await form.getByRole('button', { name: 'Choose an option *', exact: true }).click()
        await page.getByRole('menuitemradio', { name: 'Red', exact: true }).filter({ visible: true }).click()
        // A question form names its positive action Approve, and a permission names it Allow.
        const approve = controlButton(page, 'allow')
        await expect(approve).toHaveText('Approve')
        await approve.click()
      })
      expect(answer).toContain('Red')
      expect(answer).not.toContain('Blue')
      // The question reached the browser as the one native elicitation request of Dirac. The read of the controls also
      // throws the failure of the watch: an invalid control, or a stream that ended before the answer.
      diracQuestionControl(watch.controls(), DIRAC_COLOR_QUESTION)
    }
    finally {
      // Read before the cancel, because the end of the stream fails the watch.
      const report = controlsReport(watch)
      watch.cancel()
      await testInfo.attach('dirac-native-question-controls', { body: report, contentType: 'application/json' })
    }
  })
})

diracTest('returns a typed answer and refuses empty or whitespace-only text', async ({ askingDiracWorkspace, page, leapmuxServer, modelScript }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingDiracWorkspace.workspaceId })
  const result = await exerciseQuestionReply(context, async (form) => {
    await form.getByRole('button', { name: 'Choose an option *', exact: true }).click()
    await page.getByRole('menuitemradio', { name: 'Other answer', exact: true }).filter({ visible: true }).click()
    const approve = controlButton(page, 'allow')
    await approve.click()
    await expect(form).toContainText('Answer')
    const answer = form.getByLabel('Answer *', { exact: true })
    await expect(approve).toBeDisabled()
    await answer.fill('   ')
    await expect(approve).toBeDisabled()
    await answer.fill('  Green  ')
    await expect(approve).toBeEnabled()
    await approve.click()
  })
  expect(result).toContain('<answer>\nGreen\n</answer>')
  expect(result).not.toContain('  Green  ')
  expect(result).not.toContain('Blue')
  expect(result).not.toContain('Red')
})

diracTest('returns native question cancellation without selecting an offered answer', async ({ askingDiracWorkspace, page, leapmuxServer, modelScript }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingDiracWorkspace.workspaceId })
  const result = await exerciseQuestionReply(context, async () => {
    await page.locator('[data-testid="control-more-actions"]:visible').click()
    await page.getByRole('menuitem', { name: 'Cancel', exact: true }).filter({ visible: true }).click()
  })
  expect(result).toContain('The user declined to answer the follow-up question.')
  expect(result).not.toContain('<answer>')
})
