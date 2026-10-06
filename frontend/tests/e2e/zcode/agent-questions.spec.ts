import { expect } from '@playwright/test'
import { exerciseQuestionAnswer } from '../helpers/nativeQuestion'
import { controlButton, openWorkspace } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { zcodeTest } from '../zcode-fixtures'
import { nativeContext, ZCODE_AGENT } from './scenarios'

zcodeTest('renders ZCode native question descriptions and selects an answer', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
  await page.setViewportSize({ width: 600, height: 900 })
  await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, ZCODE_AGENT, { directoryPrefix: 'renderer-zcode-question-' })
  const diagram = '┌────────┐\n│ sample │\n└────────┘'
  await page.reload()
  await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  // The agent's OWN question extension raises this control request, from a
  // scripted tool call. Seeding the request row instead skipped the extension
  // entirely, so nothing proved that ZCode's own payload reaches this surface.
  const { result } = await exerciseQuestionAnswer(context, {
    // No `value` on an option: ZCode's own schema refuses it with
    // `InputValidationError: An unexpected parameter \`value\` was provided`,
    // which the agent reports as a failed tool call and then talks past.
    questions: [{ question: 'Pick a color.', header: 'Color', options: [{ label: 'Blue', description: 'Choose the color blue.', preview: diagram }, { label: 'Green', description: 'Choose the color green.', preview: '```ts\nconst color = "green"\n```' }] }],
    callId: 'color-question',
    prompt: 'Ask me to pick a color.',
    answer: 'The choice was recorded.',
    reply: async (banner) => {
      await expect(banner.getByText('Choose the color blue.', { exact: true })).toBeVisible()
      await expect(banner.getByText('Choose the color green.', { exact: true })).toBeVisible()
      const preview = banner.getByRole('region', { name: 'Blue preview' })
      await expect(preview).toContainText('│ sample │')
      await expect(preview).toHaveCSS('white-space', 'pre')
      await expect(banner.getByRole('region', { name: 'Green preview' }).locator('pre code')).toContainText('const color = "green"')
      await expect(banner.getByRole('region', { name: 'Green preview' })).toBeInViewport()
      expect(await banner.evaluate(element => element.scrollWidth <= element.clientWidth)).toBe(true)
      const option = banner.getByTestId('question-option-Green')
      await option.click()
      await expect(option.getByRole('radio')).toBeChecked()
      const submit = controlButton(page, 'submit')
      await expect(submit).toBeEnabled()
      await submit.click()
    },
  })
  expect(result).toContain('Green')
  expect(result).not.toContain('Blue')
})
