import { screen, within } from '@solidjs/testing-library'

/**
 * Reading a control-request banner's pill groups, for the suites that drive a
 * permission or plan-approval banner.
 *
 * Both groups are written by
 * `~/components/chat/controls/ControlPillGroups`, which owns their accessible
 * labels ("Permissions", "Allow scope", "Allow as"). The locator is specified once here, so a
 * rename cannot be applied to some suites and missed in others.
 */

/** The permission preset group (Unchanged / Smart / Bypass). */
export function permissionPillGroup() {
  return within(screen.getByRole('radiogroup', { name: 'Permissions' }))
}

/** An allow-choice group, identified by its provider-facing label. */
export function allowChoicePillGroup(label: 'Allow scope' | 'Allow as' = 'Allow scope') {
  return within(screen.getByRole('radiogroup', { name: label }))
}
