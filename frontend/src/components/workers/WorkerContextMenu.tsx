import type { Component } from 'solid-js'
import type { WorkerInfo } from '~/lib/workerInfoCache'
import MoreHorizontal from 'lucide-solid/icons/more-horizontal'
import { Show } from 'solid-js'
import { isTunnelAvailable } from '~/api/platformBridge'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { IconButton } from '~/components/common/IconButton'
import { showInfoToast } from '~/components/common/Toast'
import { formatBuildTime } from '~/lib/systemInfo'
import { menuTrigger } from '~/components/tree/sidebarActions.css'
import { isSoloMode } from '~/lib/systemInfo'
import { dangerMenuItem } from '~/styles/shared.css'

interface WorkerContextMenuProps {
  workerInfo: WorkerInfo | null
  isOwner: boolean
  hasTunnels: boolean
  onAddTunnel: () => void
  onDeleteAllTunnels: () => void
  onDeregister: () => void
}

export const WorkerContextMenu: Component<WorkerContextMenuProps> = (props) => {
  const infoText = () => {
    const info = props.workerInfo
    if (!info)
      return null
    let versionText = info.version
    if (info.commitHash)
      versionText += ` (${info.commitHash})`
    const buildTime = formatBuildTime(info.buildTime)
    if (buildTime)
      versionText += `, built ${buildTime}`
    return `${versionText}, ${info.os} (${info.arch})`
  }

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
      <Show when={infoText()}>
        {text => (
          <button
            role="menuitem"
            onClick={() => {
              navigator.clipboard.writeText(text())
              showInfoToast('Worker info copied to clipboard')
            }}
          >
            {text()}
          </button>
        )}
      </Show>
      <Show when={isTunnelAvailable() && props.isOwner}>
        <button role="menuitem" onClick={() => props.onAddTunnel()}>
          Add tunnel...
        </button>
        <Show when={props.hasTunnels}>
          <button role="menuitem" class={dangerMenuItem} onClick={() => props.onDeleteAllTunnels()}>
            Delete all tunnels...
          </button>
        </Show>
      </Show>
      <Show when={!isSoloMode()}>
        <hr />
        <button role="menuitem" class={dangerMenuItem} onClick={() => props.onDeregister()}>
          Deregister...
        </button>
      </Show>
    </DropdownMenu>
  )
}
