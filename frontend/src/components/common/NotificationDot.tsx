import type { Component } from 'solid-js'
import * as styles from './NotificationDot.css'

/**
 * The dot that says "this row moved while you were somewhere else".
 *
 * One component for every surface, so the tab strip and the sidebar cannot
 * drift apart in look or in markup. A tab row shows it for its own
 * `hasNotification`; a COLLAPSED sidebar group shows it for any tab under it,
 * which is the only way the marker reaches a user who has that group folded.
 *
 * DECORATIVE. `aria-hidden` keeps it out of the enclosing row's accessible
 * name: the row is a tab or a group, and a nameless span appended to that name
 * would say nothing to a screen reader while breaking every `getByRole(...,
 * { name })` lookup that reads it. No surface states the unseen activity to
 * assistive technology today -- a strip tab carries `role="tab"`, `aria-selected`
 * and its label alone, and a sidebar leaf carries no role at all -- so a screen
 * reader announces nothing when the dot appears. Removing `aria-hidden` does
 * not repair that; an announcement needs an offscreen label or a live region,
 * which is its own change.
 *
 * `testId` is a two-member union rather than a plain string, and it identifies
 * the FAMILY: one id for the strip, which both of its rows share, and one for
 * the sidebar, which every row shares along with the section header and the
 * rail. A test reaches a single surface by scoping through the enclosing row's
 * own id, which every locator in the suite already does. The union is what
 * makes a typo, or an invented third id, a compile error rather than a dot no
 * locator ever finds.
 */
export const NotificationDot: Component<{
  testId: 'tab-notification' | 'sidebar-tab-notification'
}> = props => (
  <span class={styles.notificationDot} data-testid={props.testId} aria-hidden="true" />
)
