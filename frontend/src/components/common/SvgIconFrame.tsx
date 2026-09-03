import type { LucideProps } from 'lucide-solid'
import type { JSX } from 'solid-js'
import { splitProps } from 'solid-js'

/**
 * True when the caller gave the glyph a role or an accessible name.
 *
 * The key's PRESENCE decides, not its value. That is the rule lucide-solid
 * applies to its own icons, and copying it is the point: a hand-drawn glyph and
 * a lucide glyph that swap places in one `icon` prop must reach a screen reader
 * the same way. A call site that wants no role passes NO key, because
 * `role={undefined}` counts as present and drops the `aria-hidden` below.
 */
function hasA11yProp(props: object): boolean {
  for (const key in props) {
    if (key === 'role' || key === 'title' || key.startsWith('aria-'))
      return true
  }
  return false
}

/**
 * Shared 24x24 SVG frame for hand-drawn icons that want to match the
 * visual language of lucide-solid (stroke="currentColor", rounded caps,
 * strokeWidth defaulting to 2). Callers supply the inner paths/rects/lines
 * as children.
 *
 * It also matches lucide's ACCESSIBILITY default: a glyph with no role and no
 * `aria-*` prop is decoration, so it carries `aria-hidden="true"` and stays out
 * of the accessibility tree. Almost every call site is a glyph inside a button
 * that a tooltip already names, where a second nameless node is noise.
 *
 * lucide skips its `aria-hidden` when the caller passes children, because there
 * children are extra content inside a complete icon. This frame takes the icon
 * ITSELF as children, so that clause does not carry over -- it would leave
 * every hand-drawn glyph unhidden.
 *
 * `aria-hidden` sits before the spread, so a caller that passes its own value
 * still wins.
 */
export function SvgIconFrame(props: LucideProps & { children: JSX.Element }) {
  const [local, rest] = splitProps(props, ['size', 'color', 'strokeWidth', 'class', 'children'])
  const size = () => local.size ?? 24
  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      width={size()}
      height={size()}
      viewBox="0 0 24 24"
      fill="none"
      stroke={local.color ?? 'currentColor'}
      stroke-width={local.strokeWidth ?? 2}
      stroke-linecap="round"
      stroke-linejoin="round"
      class={local.class}
      aria-hidden={hasA11yProp(rest) ? undefined : 'true'}
      {...rest}
    >
      {local.children}
    </svg>
  )
}
