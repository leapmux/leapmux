import type { JSX } from 'solid-js'
import type { IconSizeName } from './Icon'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import { spinner } from '~/styles/animations.css'
import { Icon } from './Icon'

export interface SpinnerProps {
  /** Token-driven icon size. Defaults to `sm` (16px). */
  'size'?: IconSizeName
  /** Optional test id forwarded to the icon root. */
  'data-testid'?: string
  /**
   * Optional inline style overrides. Most callers don't need this; pass
   * only when the surrounding layout truly requires extra styling beyond
   * what {@link Icon} already provides (flex-shrink + min-width/height).
   */
  'style'?: JSX.CSSProperties
}

/**
 * A LoaderCircle rendered at the design-system size scale with the shared
 * spin animation. Use this anywhere a "work-in-flight" indicator is
 * needed — submit buttons, loading sections, dialog blockers — so the
 * three pieces (icon, animation class, sizing) stay centralised.
 */
export function Spinner(props: SpinnerProps) {
  return (
    <Icon
      icon={LoaderCircle}
      size={props.size ?? 'sm'}
      class={spinner}
      data-testid={props['data-testid']}
      style={props.style}
    />
  )
}
