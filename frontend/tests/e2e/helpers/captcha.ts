import type { Page } from '@playwright/test'
import { expect } from '@playwright/test'

/** Wait for form readiness and solve the ALTCHA widget when the hub requires it. */
export async function solveCaptchaViaUI(page: Page): Promise<void> {
  const widget = page.locator('altcha-widget')
  const submit = page.locator('form button[type="submit"]')
  // A missing widget can mean that bootstrap has not answered yet.
  // An enabled submit button proves readiness when the hub disables captcha.
  await expect.poll(async () => {
    if (await widget.count() > 0)
      return true
    return await submit.count() > 0 && await submit.first().isEnabled()
  }, { message: 'solveCaptchaViaUI: neither the captcha widget nor an enabled submit button appeared' }).toBe(true)
  if (await widget.count() === 0)
    return
  const checkbox = widget.locator('input[type="checkbox"]')
  await checkbox.waitFor({ state: 'visible' })
  if (await checkbox.isChecked())
    return
  // The checkmark overlaps the input. Force the click to reach the input itself.
  await checkbox.click({ force: true })
  await expect(checkbox).toBeChecked()
}
