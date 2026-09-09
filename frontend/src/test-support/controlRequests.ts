import { screen, within } from '@solidjs/testing-library'

/**
 * Reading a control-request banner's pill groups, for the suites that drive a
 * permission or plan-approval banner.
 *
 * Both groups are written by
 * `~/components/chat/controls/ControlPillGroups`. That component owns the
 * "Permissions" name. Each CALLER owns its own allow-choice name, so the
 * locator here takes the name as an argument:
 *
 * - "Allow scope" — `~/components/chat/controls/PermissionDecisionActions`.
 * - "Allow as" — `ALLOW_AS_LABEL` in
 *   `~/components/chat/providers/codex/CodexControlRequest`.
 *
 * Each of those spells its name ONCE, so a rename there reaches every site that
 * renders the group. It does NOT reach this file: the union below is a separate
 * spelling, and a rename must update it too.
 */

/** The permission preset group (Unchanged / Smart / Bypass). */
export function permissionPillGroup() {
  return within(screen.getByRole('radiogroup', { name: 'Permissions' }))
}

/** An allow-choice group, identified by its provider-facing label. */
export function allowChoicePillGroup(label: 'Allow scope' | 'Allow as' = 'Allow scope') {
  return within(screen.getByRole('radiogroup', { name: label }))
}
