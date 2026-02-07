import type { Component } from 'solid-js'
import Ellipsis from 'lucide-solid/icons/ellipsis'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { IconButton } from '~/components/common/IconButton'
import { dangerMenuItem } from '~/styles/shared.css'
import { iconSize } from '~/styles/tokens'

interface WorkerContextMenuProps {
  onRename: () => void
  onSharing: () => void
  onDeregister: () => void
}

export const WorkerContextMenu: Component<WorkerContextMenuProps> = (props) => {
  return (
    <DropdownMenu
      trigger={triggerProps => (
        <IconButton icon={Ellipsis} iconSize={iconSize.md} size="lg" data-testid="worker-menu-trigger" {...triggerProps} />
      )}
    >
      <button role="menuitem" onClick={() => props.onRename()}>
        Rename
      </button>
      <button role="menuitem" onClick={() => props.onSharing()}>
        Sharing
      </button>
      <hr />
      <button role="menuitem" class={dangerMenuItem} onClick={() => props.onDeregister()}>
        Deregister
      </button>
    </DropdownMenu>
  )
}
