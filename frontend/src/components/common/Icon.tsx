import type { LucideIcon } from 'lucide-solid'
import type { JSX } from 'solid-js'
import { iconSize } from '~/styles/tokens'

export type IconSizeName = 'xxs' | 'xs' | 'sm' | 'md' | 'lg' | 'xl'

export interface IconProps {
  icon: LucideIcon
  size: IconSizeName
  class?: string
  style?: JSX.CSSProperties
}

export function Icon(props: IconProps) {
  const px = () => iconSize[props.size]
  return <props.icon size={px()} class={props.class} style={props.style} />
}
