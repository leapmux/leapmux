import type { JSX } from 'solid-js'
import { For, Show } from 'solid-js'
import { SubMenu } from '~/components/common/SubMenu'
import { menuSectionHeader } from '~/styles/shared.css'

export interface RepositoryTargetMenuProps<T> {
  /** What the user picks between: repositories, or checkouts of one. */
  targets: () => readonly T[]
  /** The row label, and the submenu label, for one target. */
  labelOf: (target: T) => string
  /** Heading over the list, used only when there is more than one target. */
  header: string
  /** Everything the user can do to one target. */
  children: (target: T) => JSX.Element
  /**
   * Prefix for the per-target submenu test ids. Each target's own id appends
   * a slug of its label, because a shared id would address whichever copy the
   * DOM holds first once a workspace spans two repositories -- which is the
   * only case that renders a submenu at all.
   */
  testIdPrefix: string
}

/**
 * A label reduced to something addressable: lowercase, and every run of
 * non-alphanumerics folded to one hyphen. Repository labels carry spaces,
 * dots, slashes and a middle dot, none of which belong in a selector.
 */
function slugify(label: string): string {
  return label.toLowerCase().replace(/[^a-z0-9]+/gi, '-').replace(/^-|-$/g, '')
}

/**
 * Repository first, then actions.
 *
 * With ONE target the actions render flat, because a submenu holding the only
 * choice is a click nobody should have to make. With more than one, each
 * target opens a submenu that holds every action on it.
 *
 * The alternative -- one submenu per ACTION, each listing the repositories --
 * is what the workspace row menu used to do, and it scattered a single
 * repository's actions across three separate submenus. This shape asks the
 * question the user actually has first: which repository?
 *
 * Zero targets render nothing at all, so a caller can pass an unfiltered list
 * and let this decide.
 */
export function RepositoryTargetMenu<T>(props: RepositoryTargetMenuProps<T>): JSX.Element {
  return (
    <Show when={props.targets().length > 0}>
      <Show
        when={props.targets().length > 1}
        fallback={props.children(props.targets()[0])}
      >
        <li class={menuSectionHeader}>{props.header}</li>
        <For each={props.targets()}>
          {target => (
            <SubMenu
              label={props.labelOf(target)}
              data-testid={`${props.testIdPrefix}-${slugify(props.labelOf(target))}`}
              popoverTestId={`${props.testIdPrefix}-${slugify(props.labelOf(target))}-popover`}
            >
              {props.children(target)}
            </SubMenu>
          )}
        </For>
      </Show>
    </Show>
  )
}
