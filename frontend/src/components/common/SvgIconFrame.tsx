import type { LucideProps } from 'lucide-solid'
import type { JSX } from 'solid-js'
import { splitProps } from 'solid-js'

/**
 * Shared 24x24 SVG frame for hand-drawn icons that want to match the
 * visual language of lucide-solid (stroke="currentColor", rounded caps,
 * strokeWidth defaulting to 2). Callers supply the inner paths/rects/lines
 * as children.
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
      {...rest}
    >
      {local.children}
    </svg>
  )
}
