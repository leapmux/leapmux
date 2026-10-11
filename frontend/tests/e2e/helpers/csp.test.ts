import type { ConsoleMessage, Page } from '@playwright/test'
import { describe, expect, it } from 'vitest'
import { collectCspViolations, isCspViolation } from './csp'

/** A page that hands each listener the console messages that the test emits. */
function consolePage() {
  const listeners: Array<(message: ConsoleMessage) => void> = []
  const page = {
    on: (event: string, listener: (message: ConsoleMessage) => void) => {
      expect(event).toBe('console')
      listeners.push(listener)
    },
  } as unknown as Page
  const emit = (text: string) => {
    for (const listener of listeners)
      listener({ text: () => text } as ConsoleMessage)
  }
  return { page, emit }
}

describe('isCspViolation', () => {
  it.each([
    'Refused to load the image \'https://x.test/a.png\' because it violates the following Content Security Policy directive',
    'Refused to execute inline script because it violates the following Content Security Policy directive: "script-src \'self\'"',
    'Content Security Policy: The page settings blocked the loading of a resource',
  ])('reads a violation report: %s', (text) => {
    expect(isCspViolation(text)).toBe(true)
  })

  it('ignores an unrelated message', () => {
    expect(isCspViolation('[vite] connected.')).toBe(false)
  })
})

describe('collectCspViolations', () => {
  it('records each violation from the start of the collection, in arrival order, and skips other messages', () => {
    const { page, emit } = consolePage()
    const violations = collectCspViolations(page)
    expect(violations).toEqual([])
    emit('Refused to load the font \'https://fonts.test/a.woff2\'')
    emit('the app booted')
    emit('Content Security Policy: blocked a worker')
    expect(violations).toEqual(['Refused to load the font \'https://fonts.test/a.woff2\'', 'Content Security Policy: blocked a worker'])
  })
})
