import type { Locator, Page } from '@playwright/test'
import { expect } from '@playwright/test'

/** Fill the disposable MCP server's form with values that expose false and zero. */
export async function fillMcpProbeForm(page: Page): Promise<Locator> {
  const form = page.getByTestId('elicitation-form').filter({ visible: true })
  await expect(form).toBeVisible()
  await form.getByLabel('Count *').fill('0')
  await form.getByRole('button', { name: 'Enabled *', exact: true }).click()
  await page.getByRole('menuitemradio', { name: 'No', exact: true }).click()
  await form.getByRole('button', { name: 'Color *', exact: true }).click()
  await page.getByRole('menuitemradio', { name: 'Blue', exact: true }).click()
  return form
}
