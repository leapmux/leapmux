import type { Page } from '@playwright/test'
import type { MockModelMatcher, MockModelPattern } from '../helpers/mockModelScript'
import { applyPermissionPreset, expectSettingsChip, waitForSettingsHydrated } from '../helpers/ui'

/** The native child reminder remains in history when a later user turn follows it. */
export const KIMI_SUBAGENT_REMINDER = 'You are running as a subagent:'

/** Match the own turn of a Kimi Code child whose request body matches `body`. */
export function kimiChildTurn(body: MockModelPattern): MockModelMatcher {
  return {
    body: [
      KIMI_SUBAGENT_REMINDER,
      ...(Array.isArray(body)
        ? body
        : [body]),
    ],
  }
}

/**
 * Prepare a Kimi Code agent for a child run: wait for its settings, and apply the Bypass preset.
 * A command of the child stops at a banner under Always Ask, and a subagent spec proves the routing, not the approval.
 */
export async function prepareKimiChildRun(page: Page): Promise<void> {
  await waitForSettingsHydrated(page)
  await applyPermissionPreset(page, 'bypass')
  await expectSettingsChip(page, 'Never Ask')
}
