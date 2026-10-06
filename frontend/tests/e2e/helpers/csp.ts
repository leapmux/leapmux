import type { ConsoleMessage, Page } from '@playwright/test'

/** Whether a console message reports a Content Security Policy violation. */
export function isCspViolation(text: string): boolean {
  return text.includes('Content Security Policy') || text.includes('Refused to')
}

/**
 * Record every Content Security Policy violation that the browser reports on `page` from now on, in arrival order.
 * The returned array fills as the page runs, so read it after the step under test.
 *
 * The console message is the one signal that covers both a blocked resource and a blocked inline script. A
 * `securitypolicyviolation` listener must run in the page, and a blocked script can stop it from installing.
 */
export function collectCspViolations(page: Page): string[] {
  const violations: string[] = []
  page.on('console', (message: ConsoleMessage) => {
    const text = message.text()
    if (isCspViolation(text))
      violations.push(text)
  })
  return violations
}
