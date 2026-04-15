import type { LucideProps } from 'lucide-solid'
import { splitProps } from 'solid-js'
import { SvgIconFrame } from '~/components/common/SvgIconFrame'

function SidebarIcon(props: LucideProps & { divider: string, fill: string }) {
  const [local, rest] = splitProps(props, ['divider', 'fill'])
  return (
    <SvgIconFrame {...rest}>
      <path d={local.fill} fill="currentColor" fill-opacity="0.35" stroke="none" />
      <rect width="18" height="18" x="3" y="3" rx="2" />
      <path d={local.divider} />
    </SvgIconFrame>
  )
}

export function PanelLeftFilled(props: LucideProps) {
  return (
    <SidebarIcon
      divider="M9 3v18"
      fill="M5 3H9V21H5A2 2 0 0 1 3 19V5A2 2 0 0 1 5 3Z"
      {...props}
    />
  )
}

export function PanelRightFilled(props: LucideProps) {
  return (
    <SidebarIcon
      divider="M15 3v18"
      fill="M15 3H19A2 2 0 0 1 21 5V19A2 2 0 0 1 19 21H15Z"
      {...props}
    />
  )
}
