import type { Component } from 'solid-js'
import type { IconSizeName } from '~/components/common/Icon'
import RefreshCw from 'lucide-solid/icons/refresh-cw'
import { createSignal } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { refreshButton } from '~/styles/shared.css'
import * as styles from './RefreshButton.css'

interface RefreshButtonProps {
  onClick: () => void
  disabled?: boolean
  title?: string
  size?: IconSizeName
}

export const RefreshButton: Component<RefreshButtonProps> = (props) => {
  const [animating, setAnimating] = createSignal(false)

  const handleClick = () => {
    setAnimating(true)
    props.onClick()
  }

  return (
    <button
      type="button"
      class={refreshButton}
      onClick={handleClick}
      disabled={props.disabled}
      title={props.title}
    >
      <Icon
        icon={RefreshCw}
        size={props.size ?? 'sm'}
        class={animating() ? styles.spinning : ''}
        onAnimationEnd={() => setAnimating(false)}
      />
    </button>
  )
}
