import type { LucideIcon, LucideProps } from 'lucide-solid'
import { splitProps } from 'solid-js'
import { iconSize } from '~/styles/tokens'

export type IconSizeName = 'xxs' | 'xs' | 'sm' | 'md' | 'lg' | 'xl'

export interface IconProps extends Omit<LucideProps, 'size'> {
  icon: LucideIcon
  size: IconSizeName
}

export function Icon(props: IconProps) {
  const [local, rest] = splitProps(props, ['icon', 'size'])
  const px = () => iconSize[local.size]
  return <local.icon size={px()} style={{ 'flex-shrink': '0', 'min-width': `${px()}px`, 'min-height': `${px()}px` }} {...rest} />
}
