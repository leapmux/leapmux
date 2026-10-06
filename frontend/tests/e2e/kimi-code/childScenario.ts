import type { Page } from '@playwright/test'
import type { MockModelMatcher, MockModelPattern } from '../helpers/mockModelScript'
import { applyPermissionPreset, expectSettingsChip, waitForSettingsHydrated } from '../helpers/ui'

/**
 * The statement that opens the system prompt of a Kimi Code subagent, and that the system prompt of the main agent
 * never holds. A rule on it answers the child alone, although the requests of the root quote the child prompt in the
 * spawn call.
 */
export const KIMI_SUBAGENT_SYSTEM = 'You are now running as a subagent'

/** Match the own turn of a Kimi Code child whose request body matches `body`. */
export function kimiChildTurn(body: MockModelPattern): MockModelMatcher {
  return { system: KIMI_SUBAGENT_SYSTEM, body }
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
