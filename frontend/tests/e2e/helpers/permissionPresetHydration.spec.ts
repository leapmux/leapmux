import { expect, test } from '../fixtures'
import { applyPermissionPreset } from './ui'

test.describe('applyPermissionPreset', () => {
  test('reopens the menu after a permission shortcut arrives', async ({ page }) => {
    await page.setContent(`
      <button type="button" data-testid="composer-plus-trigger" aria-expanded="false">Add</button>
      <menu popover="auto" data-testid="composer-plus-popover">
        <button type="button" data-testid="composer-group-permissionMode">Permissions</button>
      </menu>
    `)
    await page.evaluate(() => {
      const trigger = document.querySelector<HTMLButtonElement>('[data-testid="composer-plus-trigger"]')!
      const menu = document.querySelector<HTMLElement>('[data-testid="composer-plus-popover"]')!
      let opens = 0
      trigger.addEventListener('click', () => menu.togglePopover())
      menu.addEventListener('toggle', () => {
        const open = menu.matches(':popover-open')
        trigger.setAttribute('aria-expanded', String(open))
        if (open) {
          opens++
          document.body.dataset.menuOpens = String(opens)
          return
        }
        if (opens === 1) {
          const action = document.createElement('button')
          action.type = 'button'
          action.dataset.testid = 'composer-bypass-permissions'
          action.textContent = 'Bypass permissions'
          action.addEventListener('click', () => {
            document.body.dataset.bypassClicks = String(Number(document.body.dataset.bypassClicks ?? '0') + 1)
          })
          menu.append(action)
        }
      })
    })

    await applyPermissionPreset(page, 'bypass')

    await expect(page.locator('body')).toHaveAttribute('data-menu-opens', '2')
    await expect(page.locator('body')).toHaveAttribute('data-bypass-clicks', '1')
  })
})
