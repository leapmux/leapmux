import type { LucideIcon } from 'lucide-solid'
import type { Component, ComponentProps, JSX } from 'solid-js'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import { Show, splitProps } from 'solid-js'
import { spinner } from '~/styles/animations.css'
import * as styles from './IconButton.css'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

export enum IconButtonState {
  Enabled = 'enabled',
  Active = 'active',
  Disabled = 'disabled',
  Loading = 'loading',
}

export type IconButtonSize = 'sm' | 'md' | 'lg' | 'xl'

type ButtonHtmlAttributes = Omit<
  ComponentProps<'button'>,
  'class' | 'style' | 'disabled' | 'onClick' | 'children'
>

export interface IconButtonProps extends ButtonHtmlAttributes {
  /** The lucide-solid icon component to render. */
  icon: LucideIcon
  /** Icon size in pixels. Default: 14 */
  iconSize?: number
  /** Container size token. Default: none (intrinsic sizing). */
  size?: IconButtonSize
  /** Button state. Default: Enabled */
  state?: IconButtonState
  /** Additional CSS class(es). */
  class?: string
  /** Inline styles. */
  style?: JSX.CSSProperties
  /** Click handler. */
  onClick?: JSX.EventHandlerUnion<HTMLButtonElement, MouseEvent>
  /** Extra content rendered after the icon (e.g., dropdown arrow). */
  children?: JSX.Element
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

const sizeClassMap: Record<IconButtonSize, string> = {
  sm: styles.sizeSm,
  md: styles.sizeMd,
  lg: styles.sizeLg,
  xl: styles.sizeXl,
}

export const IconButton: Component<IconButtonProps> = (props) => {
  const [local, rest] = splitProps(props, [
    'icon',
    'iconSize',
    'size',
    'state',
    'class',
    'style',
    'onClick',
    'children',
  ])

  const state = () => local.state ?? IconButtonState.Enabled
  const iconPx = () => local.iconSize ?? 14
  const isDisabled = () =>
    state() === IconButtonState.Disabled || state() === IconButtonState.Loading
  const isLoading = () => state() === IconButtonState.Loading
  const isActive = () => state() === IconButtonState.Active

  const classes = () => {
    const parts: string[] = [styles.base]
    if (isActive())
      parts.push(styles.active)
    if (local.size)
      parts.push(sizeClassMap[local.size])
    if (local.class)
      parts.push(local.class)
    return parts.join(' ')
  }

  return (
    <button
      type="button"
      class={classes()}
      style={local.style}
      disabled={isDisabled()}
      // eslint-disable-next-line solid/reactivity -- onClick is forwarded from props, reactivity not needed
      onClick={local.onClick}
      {...rest}
    >
      <Show when={isLoading()} fallback={<local.icon size={iconPx()} />}>
        <LoaderCircle size={iconPx()} class={spinner} />
      </Show>
      {local.children}
    </button>
  )
}
