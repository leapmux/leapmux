import { expect } from '@playwright/test'
import { test } from './fixtures'
import { expectRowBecomesFinal } from './helpers/subagentRegistry'

for (const secondary of [false, true]) {
  test(`reads a finished registry status with a ${secondary ? 'secondary label' : 'title label'}`, async ({ page }) => {
    await page.setContent(`<div data-testid="section-header-background_tasks"><button role="button">Background tasks</button><div data-testid="bg-task-row" data-status="succeeded"><div><span data-testid="bg-task-title">${secondary ? 'Run the command' : 'Succeeded'}</span><span data-testid="bg-task-status-dot" aria-label="Succeeded" style="display:inline-block;width:12px;height:12px"></span></div>${secondary ? '<span data-testid="bg-task-secondary">Succeeded</span>' : ''}</div></div>`)
    await expectRowBecomesFinal(page, page.locator('[data-testid="bg-task-row"]:visible').first())
  })
}

test('restores a task section that collapses after its final status arrives', async ({ page }) => {
  await page.setContent(`<div id="section" data-testid="section-header-background_tasks"><button role="button">Background tasks</button><div id="task" data-testid="bg-task-row" data-status="succeeded"><span data-testid="bg-task-title">Succeeded</span><span id="dot" data-testid="bg-task-status-dot" aria-label="Succeeded" style="display:inline-block;width:12px;height:12px"></span></div></div>`)
  await page.evaluate(() => {
    const section = document.getElementById('section')!
    const row = document.getElementById('task')!
    section.querySelector('button')!.addEventListener('click', () => {
      section.removeAttribute('data-closed')
      row.hidden = false
    })
  })
  const row = page.locator('[data-testid="bg-task-row"]:visible').first()
  let collapsed = false
  const collapse = async () => {
    if (collapsed)
      return
    collapsed = true
    await page.evaluate(() => {
      document.getElementById('section')!.setAttribute('data-closed', '')
      document.getElementById('task')!.hidden = true
    })
  }
  const readAttribute = row.getAttribute.bind(row)
  row.getAttribute = async (name, options) => {
    const value = await readAttribute(name, options)
    if (name === 'data-status')
      await collapse()
    return value
  }
  const readAll = row.evaluateAll.bind(row)
  row.evaluateAll = async (read, argument) => {
    await collapse()
    return readAll(read, argument)
  }
  await expectRowBecomesFinal(page, row)
  expect(collapsed).toBe(true)
  await expect(row).toBeVisible()
})
