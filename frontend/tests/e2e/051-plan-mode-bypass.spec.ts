import { expect, test } from './fixtures'
import { ENTER_PLAN_PROMPT, enterAndExitPlanMode, EXIT_PLAN_PROMPT } from './helpers/plan-mode'
import { expectSettingsChip, sendMessage, settingsBar, waitForAgentIdle, waitForControlBanner, waitForSettingsIdle } from './helpers/ui'

test.describe('plan mode - bypass permissions', () => {
  test('bypass permissions from ExitPlanMode banner', async ({ page, authenticatedWorkspace }) => {
    const trigger = settingsBar(page)
    await expect(trigger).toBeVisible()
    await expectSettingsChip(page, 'Default')

    // Step 1: Enter plan mode and write a dummy plan
    await sendMessage(page, ENTER_PLAN_PROMPT)

    // Verify dropdown switches to Plan Mode (EnterPlanMode is auto-approved)
    await expectSettingsChip(page, 'Plan Mode')
    await waitForAgentIdle(page)

    // Step 2: Exit plan mode (produces control_request banner)
    await sendMessage(page, EXIT_PLAN_PROMPT)
    const banner = await waitForControlBanner(page)
    await expect(banner.getByText('Plan Ready for Review')).toBeVisible()

    // Verify the switch and the permission pills are visible, with Smart selected.
    const clearContextSwitch = page.locator('[data-testid="plan-clear-context-checkbox"] input[type="checkbox"]')
    await expect(clearContextSwitch).toBeVisible()
    await expect(clearContextSwitch).not.toBeChecked()

    const permissionPill = page.getByRole('radiogroup', { name: 'Permissions' })
    const bypassRadio = permissionPill.getByRole('radio', { name: 'Bypass' })
    await expect(permissionPill.getByRole('radio', { name: 'Unchanged' })).toBeVisible()
    // A plan approval opens on Smart, which Claude offers.
    await expect(permissionPill.getByRole('radio', { name: 'Smart' })).toBeChecked()
    await expect(permissionPill.getByRole('radio', { name: 'Unchanged' })).not.toBeChecked()
    await expect(bypassRadio).toBeVisible()
    await expect(bypassRadio).not.toBeChecked()

    // Select bypass permissions, then approve.
    await bypassRadio.click()
    await expect(bypassRadio).toBeChecked()

    const approveBtn = page.locator('[data-testid="plan-approve-btn"]')
    await expect(approveBtn).toBeEnabled()
    await approveBtn.click()

    // Verify control banner disappears (plan was approved)
    await expect(page.locator('[data-testid="control-banner"]')).not.toBeVisible()

    // Verify permission mode changed to Bypass Permissions
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Bypass Permissions')
  })

  test('approve and switches toggle with feedback on editor content', async ({ page, authenticatedWorkspace }) => {
    // Enter plan mode, write a dummy plan, and exit
    const banner = await enterAndExitPlanMode(page)
    await expect(banner.getByText('Plan Ready for Review')).toBeVisible()

    // The empty editor shows Reject, Approve, the Clear Context switch, and the permission pills.
    await expect(page.locator('[data-testid="plan-reject-btn"]')).toBeVisible()
    await expect(page.locator('[data-testid="plan-approve-btn"]')).toBeVisible()
    await expect(page.locator('[data-testid="plan-clear-context-checkbox"]')).toBeVisible()
    await expect(page.locator('[data-testid="control-permissions-pill-group"]')).toBeVisible()

    // Type rejection text in the editor
    const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
    await editor.click()
    await page.keyboard.type('needs changes', { delay: 100 })

    // With editor content: Send feedback is visible and Approve is hidden.
    await expect(page.locator('[data-testid="plan-reject-btn"]')).toHaveText('Send feedback')
    await expect(page.locator('[data-testid="plan-approve-btn"]')).not.toBeVisible()
    await expect(page.locator('[data-testid="plan-clear-context-checkbox"]')).not.toBeVisible()
    await expect(page.locator('[data-testid="control-permissions-pill-group"]')).not.toBeVisible()

    // Clear the editor
    await page.keyboard.press('Meta+a')
    await page.keyboard.press('Backspace')

    // Reject and Approve visible again
    await expect(page.locator('[data-testid="plan-reject-btn"]')).toBeVisible()
    await expect(page.locator('[data-testid="plan-approve-btn"]')).toBeVisible()
  })

  test('lays the pill radios and their moving copies out identically', async ({ page, authenticatedWorkspace }) => {
    const banner = await enterAndExitPlanMode(page)
    await expect(banner.getByText('Plan Ready for Review')).toBeVisible()

    const group = page.getByRole('radiogroup', { name: 'Permissions' })
    await expect(group).toBeVisible()

    // A serif face, far from the UA control font. A `<button>` takes its family
    // from the UA stylesheet unless the rule states `inherit`, and on some
    // platforms that family and `system-ui` resolve to the SAME face -- which
    // would let a font divergence pass unseen here. The write goes through the
    // CSSOM, which the page's content security policy does not restrict.
    await page.evaluate(() => {
      document.documentElement.style.setProperty('--font-sans', '"Times New Roman", serif')
    })
    await expect.poll(async () => group.evaluate(el =>
      getComputedStyle(el.querySelector('[role="radio"]')!).fontFamily)).toContain('Times New Roman')

    const measured = await group.evaluate((element) => {
      const metrics = (el: Element | null | undefined) => {
        if (!el)
          return undefined
        const rect = el.getBoundingClientRect()
        const style = getComputedStyle(el)
        return {
          x: rect.x,
          width: rect.width,
          font: style.fontFamily,
          fontSize: style.fontSize,
          padding: style.padding,
        }
      }
      const radios = [...element.querySelectorAll('[role="radio"]')]
      const copies = [...element.querySelectorAll('[data-pill-selection-labels] [data-label]')]
      return radios.map((radio, index) => ({
        label: radio.textContent ?? '',
        radio: metrics(radio),
        copy: metrics(copies[index]),
      }))
    })

    // The group paints each label twice: the real radio carries the text, and
    // the sliding overlay repeats it as a copy that must cover that radio
    // exactly. The fill is clipped to the RADIO, so a copy that lays out to a
    // different width drags its 1px divider off the boundary and the primary
    // window appears to spill into the next option. Every metric that decides
    // the width is compared, so a rule that reaches one row and not the other
    // fails here whatever property it sets.
    expect(measured.length).toBeGreaterThan(1)
    for (const option of measured) {
      expect(option.copy?.font).toBe(option.radio?.font)
      expect(option.copy?.fontSize).toBe(option.radio?.fontSize)
      expect(option.copy?.padding).toBe(option.radio?.padding)
      expect(Math.abs((option.copy?.x ?? 0) - (option.radio?.x ?? 0))).toBeLessThanOrEqual(0.5)
      expect(Math.abs((option.copy?.width ?? 0) - (option.radio?.width ?? 0))).toBeLessThanOrEqual(0.5)
    }
  })
})
