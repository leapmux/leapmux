import type { Page } from '@playwright/test'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fakeLocator } from '~/test-support/fakeLocator'
import { solveCaptchaViaUI } from './captcha'

/**
 * The timeout of each poll and matcher. A poll that passes ends at its first passing read, so its long limit costs
 * nothing, and a loaded run cannot end it early: Playwright reads again only after an interval of 100 ms and more. A
 * test that expects a timeout calls `expectTimeout`, so that its poll fails within the unit test.
 */
const pollLimit = vi.hoisted(() => ({ ms: 30_000 }))

// Run the real poll and matcher, each with the limit of the running test.
vi.mock('@playwright/test', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@playwright/test')>()
  const configured = () => actual.expect.configure({ timeout: pollLimit.ms })
  const expect = Object.assign(
    (...args: Parameters<typeof actual.expect>) => configured()(...args),
    { poll: (...args: Parameters<typeof actual.expect.poll>) => configured().poll(...args) },
  )
  return { ...actual, expect }
})

beforeEach(() => {
  pollLimit.ms = 30_000
})

/** Shorten the poll limit for a test that expects the poll to time out. */
function expectTimeout(): void {
  pollLimit.ms = 200
}

function form(options: { widget?: boolean, enabled?: boolean, checked?: boolean, submit?: boolean } = {}) {
  let checked = options.checked ?? false
  const checks: string[] = []
  const checkbox = fakeLocator((check) => {
    checks.push(check.expression)
    return checked
  }, {
    waitFor: vi.fn(async () => {}),
    isChecked: vi.fn(async () => checked),
    click: vi.fn(async () => { checked = true }),
    toString: () => 'captcha checkbox',
    checks,
  })
  const widget = {
    count: vi.fn(async () => options.widget ? 1 : 0),
    locator: vi.fn(() => checkbox),
  }
  const submit = {
    count: vi.fn(async () => options.submit === false ? 0 : 1),
    first: () => ({ isEnabled: async () => options.enabled ?? false }),
  }
  const page = { locator: (selector: string) => selector === 'altcha-widget' ? widget : submit } as unknown as Page
  return { page, widget, checkbox }
}

describe('captcha form readiness', () => {
  it('does not solve a captcha when the form is ready without a widget', async () => {
    const { page, checkbox } = form({ enabled: true })
    await solveCaptchaViaUI(page)
    expect(checkbox.waitFor).not.toHaveBeenCalled()
    expect(checkbox.click).not.toHaveBeenCalled()
  })

  it('waits for a late widget instead of treating its absence as disabled captcha', async () => {
    const { page, widget, checkbox } = form()
    // The first read takes 150 ms, as on a loaded host. The widget appears for the next read, which Playwright starts
    // only after its first poll interval of 100 ms.
    widget.count.mockImplementationOnce(() => new Promise<0>(resolve => setTimeout(resolve, 150, 0))).mockResolvedValue(1)
    await solveCaptchaViaUI(page)
    expect(widget.count.mock.calls.length).toBeGreaterThan(1)
    expect(checkbox.waitFor).toHaveBeenCalledWith({ state: 'visible' })
    expect(checkbox.click).toHaveBeenCalledExactlyOnceWith({ force: true })
    expect(checkbox.checks).toContain('to.be.checked')
  })

  it('keeps an already solved captcha checked', async () => {
    const { page, checkbox } = form({ widget: true, checked: true })
    await solveCaptchaViaUI(page)
    expect(checkbox.click).not.toHaveBeenCalled()
  })

  it.each([true, false])('reports a form that never becomes ready, with submit present: %s', async (submit) => {
    expectTimeout()
    const { page, checkbox } = form({ submit })
    await expect(solveCaptchaViaUI(page)).rejects.toThrow('solveCaptchaViaUI: neither the captcha widget nor an enabled submit button appeared')
    expect(checkbox.click).not.toHaveBeenCalled()
  })

  it('propagates a failure to click the widget', async () => {
    const { page, checkbox } = form({ widget: true })
    const error = new Error('widget detached')
    checkbox.click.mockRejectedValue(error)
    await expect(solveCaptchaViaUI(page)).rejects.toBe(error)
  })
})
