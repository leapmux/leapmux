import type { Component } from 'solid-js'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { rowContextMenuTrigger } from '~/components/common/moreHorizontalTrigger'
import { dangerMenuItem } from '~/styles/shared.css'

interface TunnelContextMenuProps {
  onDelete: () => void
}

export const TunnelContextMenu: Component<TunnelContextMenuProps> = (props) => {
  return (
    <DropdownMenu trigger={rowContextMenuTrigger()}>
      <button role="menuitem" class={dangerMenuItem} onClick={() => props.onDelete()}>
        Delete...
      </button>
    </DropdownMenu>
  )
}
