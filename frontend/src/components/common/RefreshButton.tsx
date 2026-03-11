import type { Component } from 'solid-js'
import type { IconButtonSize } from './IconButton'
import type { IconSizeName } from '~/components/common/Icon'
import RefreshCw from 'lucide-solid/icons/refresh-cw'
import { createSignal, splitProps } from 'solid-js'
import { IconButton, IconButtonState } from './IconButton'
import * as styles from './RefreshButton.css'

interface RefreshButtonProps {
  'onClick': () => void
  'disabled'?: boolean
  'title'?: string
  /** Icon size token. Default: 'sm' */
  'iconSize'?: IconSizeName
  /** Container size token. Default: none (intrinsic sizing). */
  'size'?: IconButtonSize
  'data-testid'?: string
}

export const RefreshButton: Component<RefreshButtonProps> = (props) => {
  const [local, rest] = splitProps(props, ['onClick', 'disabled', 'iconSize', 'size'])
  const [animating, setAnimating] = createSignal(false)

  const handleClick = () => {
    setAnimating(true)
    local.onClick()
  }

  return (
    <IconButton
      icon={RefreshCw}
      iconSize={local.iconSize ?? 'sm'}
      size={local.size}
      state={local.disabled ? IconButtonState.Disabled : IconButtonState.Enabled}
      onClick={handleClick}
      class={animating() ? styles.spinning : ''}
      onAnimationEnd={() => setAnimating(false)}
      {...rest}
    />
  )
}
