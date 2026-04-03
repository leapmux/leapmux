import type { Component } from 'solid-js'
import MoreHorizontal from 'lucide-solid/icons/more-horizontal'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { IconButton } from '~/components/common/IconButton'
import { menuTrigger } from '~/components/tree/sidebarActions.css'
import { dangerMenuItem } from '~/styles/shared.css'

interface TunnelContextMenuProps {
  onDelete: () => void
}

export const TunnelContextMenu: Component<TunnelContextMenuProps> = (props) => {
  return (
    <DropdownMenu
      trigger={triggerProps => (
        <IconButton
          icon={MoreHorizontal}
          size="sm"
          class={menuTrigger}
          onClick={(e: MouseEvent) => {
            e.stopPropagation()
            triggerProps.onClick()
          }}
          ref={triggerProps.ref}
          onPointerDown={(e: PointerEvent) => {
            e.stopPropagation()
            triggerProps.onPointerDown()
          }}
          aria-expanded={triggerProps['aria-expanded']}
        />
      )}
    >
      <button role="menuitem" class={dangerMenuItem} onClick={() => props.onDelete()}>
        Delete...
      </button>
    </DropdownMenu>
  )
}
