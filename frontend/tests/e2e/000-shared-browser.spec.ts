import type { BrowserContext, Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { storageKeys, writeEntry } from './helpers/storage'
import { loginViaToken } from './helpers/ui'

let firstPage: Page | undefined
let firstContext: BrowserContext | undefined
let leakedConsoleEvents = 0

test.describe('shared browser lifecycle', () => {
  test('leaves browser state for the cleanup boundary', async ({ page, context, leapmuxServer }) => {
    firstPage = page
    firstContext = context
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/')
    await writeEntry(page, 'leapmux:e2e:leaked', 'value', Date.now() + 60_000)
    await page.setViewportSize({ width: 777, height: 555 })
    await page.route('**/leaked-route', route => route.abort())
    page.on('console', () => leakedConsoleEvents++)
    await context.newPage()
    expect(context.pages()).toHaveLength(2)
  })

  test('reuses the tab after it removes prior test state', async ({ page, context, leapmuxServer }) => {
    expect(page).toBe(firstPage)
    expect(context).toBe(firstContext)
    expect(context.pages()).toEqual([page])
    expect(page.viewportSize()).toEqual({ width: 1280, height: 720 })
    await page.evaluate(() => console.warn('listener-cleanup-probe'))
    expect(leakedConsoleEvents).toBe(0)
    expect(await context.cookies()).toEqual([])

    const response = await page.goto(`${leapmuxServer.hubUrl}/leaked-route`)
    expect(response).not.toBeNull()
    expect(await storageKeys(page)).not.toContain('leapmux:e2e:leaked')
  })
})
