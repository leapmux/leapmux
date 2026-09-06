import type { JSX } from 'solid-js'
import { createMemo, For, Show } from 'solid-js'
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
 *
 * The fold is LOSSY, which is why {@link targetSlugs} below never trusts it
 * alone: `worker · a/b` and `worker-a-b` both reduce to `worker-a-b`, `foo_bar`
 * and `foo-bar` both reduce to `foo-bar`, and a label with no ASCII
 * alphanumerics at all reduces to the empty string.
 */
function slugify(label: string): string {
  // No `i` flag: `.toLowerCase()` already ran, and on a NEGATED class the flag
  // would stop `[^a-z0-9]` from excluding uppercase letters.
  return label.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '')
}

/**
 * One test id for each target, unique by construction.
 *
 * The readable slug is the answer wherever it is usable, because a selector a
 * person can read is worth keeping. Where two targets fold to the same slug, or
 * a slug comes out empty, the position in the list decides instead -- so the id
 * is unique for EVERY list, not only for the lists whose labels happen not to
 * collide. Uniqueness is the whole reason this prop exists, and deriving it
 * from user-controlled text cannot supply it.
 */
function targetSlugs(labels: readonly string[]): string[] {
  const slugs = labels.map(slugify)
  const counts = new Map<string, number>()
  for (const slug of slugs)
    counts.set(slug, (counts.get(slug) ?? 0) + 1)
  return slugs.map((slug, i) => (slug && counts.get(slug) === 1 ? slug : `${i}`))
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
 * and let this decide. The single-target branch reads that one target through a
 * `<Show>` rather than indexing the list, because every caller drives `targets`
 * from a memo that empties when its menu closes -- so the 1 -> 0 transition
 * happens on every close, and only Solid's disposal order kept a bare
 * `targets()[0]` from handing `undefined` to the caller's render prop.
 */
export function RepositoryTargetMenu<T>(props: RepositoryTargetMenuProps<T>): JSX.Element {
  const labels = createMemo(() => props.targets().map(t => props.labelOf(t)))
  const slugs = createMemo(() => targetSlugs(labels()))
  /** The only target, or undefined. A caller may pass a list that empties. */
  const only = () => props.targets()[0]

  return (
    <Show when={props.targets().length > 1} fallback={<Show when={only()}>{t => props.children(t())}</Show>}>
      <li class={menuSectionHeader}>{props.header}</li>
      <For each={props.targets()}>
        {(target, i) => (
          <SubMenu
            label={labels()[i()]!}
            data-testid={`${props.testIdPrefix}-${slugs()[i()]}`}
            popoverTestId={`${props.testIdPrefix}-${slugs()[i()]}-popover`}
          >
            {props.children(target)}
          </SubMenu>
        )}
      </For>
    </Show>
  )
}
