import type { Component } from 'solid-js'
import type { WorkerInfo } from '~/lib/workerInfoCache'
import MoreHorizontal from 'lucide-solid/icons/more-horizontal'
import { Show } from 'solid-js'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { IconButton } from '~/components/common/IconButton'
import { showInfoToast } from '~/components/common/Toast'
import * as listStyles from '~/components/workspace/workspaceList.css'
import { dangerMenuItem } from '~/styles/shared.css'

interface WorkerContextMenuProps {
  workerInfo: WorkerInfo | null
  onDeregister: () => void
}

export const WorkerContextMenu: Component<WorkerContextMenuProps> = (props) => {
  let popoverEl: HTMLElement | undefined

  const infoText = () => {
    const info = props.workerInfo
    if (!info)
      return null
    return `${info.version}, ${info.os} (${info.arch})`
  }

  return (
    <DropdownMenu
      popoverRef={el => popoverEl = el}
      trigger={triggerProps => (
        <IconButton
          icon={MoreHorizontal}
          size="sm"
          class={listStyles.itemMenuTrigger}
          onClick={(e: MouseEvent) => {
            e.stopPropagation()
            triggerProps.onClick()
          }}
          ref={triggerProps.ref}
          onPointerDown={(e: PointerEvent) => {
            e.stopPropagation()
            triggerProps.onPointerDown(e)
          }}
          aria-expanded={triggerProps['aria-expanded']}
        />
      )}
    >
      <Show when={infoText()}>
        {text => (
          <button
            role="menuitem"
            onClick={() => {
              navigator.clipboard.writeText(text())
              showInfoToast('Worker info copied to clipboard')
              popoverEl?.hidePopover()
            }}
          >
            {text()}
          </button>
        )}
      </Show>
      <hr />
      <button role="menuitem" class={dangerMenuItem} onClick={() => props.onDeregister()}>
        Deregister...
      </button>
    </DropdownMenu>
  )
}
