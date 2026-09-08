import { screen, within } from '@solidjs/testing-library'

/**
 * Reading a control-request banner's pill groups, for the suites that drive a
 * permission or plan-approval banner.
 *
 * Both groups are written by
 * `~/components/chat/controls/ControlPillGroups`, which owns their accessible
 * names ("Permissions", "Allow scope"). The locator is spelled once here, so a
 * rename cannot be applied to some suites and missed in others.
 */

/** The permission preset group (Default / Smart / Bypass). */
export function permissionPillGroup() {
  return within(screen.getByRole('radiogroup', { name: 'Permissions' }))
}

/** The allow-scope group (Once / Always, or Once / Session / Project). */
export function allowScopePillGroup() {
  return within(screen.getByRole('radiogroup', { name: 'Allow scope' }))
}
