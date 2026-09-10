import type { Page } from '@playwright/test'
import { describe, expect, it, vi } from 'vitest'
import { solveCaptchaViaUI } from './captcha'

// Run the real poll and matcher with a short unit-test deadline.
vi.mock('@playwright/test', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@playwright/test')>()
  return { ...actual, expect: actual.expect.configure({ timeout: 200 }) }
})

function form(options: { widget?: boolean, enabled?: boolean, checked?: boolean, submit?: boolean } = {}) {
  let checked = options.checked ?? false
  const checkbox = {
    _apiName: 'Locator',
    waitFor: vi.fn(async () => {}),
    isChecked: vi.fn(async () => checked),
    click: vi.fn(async () => { checked = true }),
    _expect: vi.fn(async () => ({ matches: checked })),
    toString: () => 'captcha checkbox',
  }
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
    widget.count.mockResolvedValueOnce(0).mockResolvedValue(1)
    await solveCaptchaViaUI(page)
    expect(widget.count.mock.calls.length).toBeGreaterThan(1)
    expect(checkbox.waitFor).toHaveBeenCalledWith({ state: 'visible' })
    expect(checkbox.click).toHaveBeenCalledExactlyOnceWith({ force: true })
    expect(checkbox._expect).toHaveBeenCalledWith('to.be.checked', expect.any(Object))
  })

  it('keeps an already solved captcha checked', async () => {
    const { page, checkbox } = form({ widget: true, checked: true })
    await solveCaptchaViaUI(page)
    expect(checkbox.click).not.toHaveBeenCalled()
  })

  it.each([true, false])('reports a form that never becomes ready, with submit present: %s', async (submit) => {
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
