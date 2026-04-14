import type { Component } from 'solid-js'
import type { WorkerInfo } from '~/lib/workerInfoCache'
import MoreHorizontal from 'lucide-solid/icons/more-horizontal'
import { Show } from 'solid-js'
import { isTunnelAvailable } from '~/api/platformBridge'
import { RelativeTime } from '~/components/chat/RelativeTime'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { IconButton } from '~/components/common/IconButton'
import { showInfoToast } from '~/components/common/Toast'
import { prettifyJson } from '~/lib/jsonFormat'
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
  const infoRows = () => {
    const info = props.workerInfo
    if (!info)
      return null
    let versionText = info.version
    if (info.commitHash)
      versionText += ` (${info.commitHash})`
    return [
      { label: 'Name:', value: info.name, kind: 'text' as const },
      { label: 'Version:', value: versionText, kind: 'text' as const },
      ...(info.buildTime ? [{ label: 'Built at:', value: info.buildTime, kind: 'relative' as const }] : []),
      { label: 'OS:', value: `${info.os} (${info.arch})`, kind: 'text' as const },
    ]
  }

  const infoJson = () => {
    const info = props.workerInfo
    if (!info)
      return null
    return prettifyJson({
      name: info.name,
      version: info.version,
      commitHash: info.commitHash || undefined,
      buildTime: info.buildTime || undefined,
      os: info.os,
      arch: info.arch,
      homeDir: info.homeDir,
    })
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
      <Show when={infoRows()}>
        {rows => (
          <button
            role="menuitem"
            style={{ 'text-align': 'left', 'line-height': '1.35' }}
            onClick={() => {
              const json = infoJson()
              if (json)
                navigator.clipboard.writeText(json)
              showInfoToast('Worker info copied to clipboard')
            }}
          >
            <span style={{ display: 'grid', 'grid-template-columns': 'max-content 1fr', gap: '0 var(--space-2)', 'align-items': 'start' }}>
              {rows().map(row => (
                <>
                  <span>{row.label}</span>
                  <span>
                    {row.kind === 'relative'
                      ? (
                          <>
                            <RelativeTime timestamp={row.value} />
                            {' ago'}
                          </>
                        )
                      : row.value}
                  </span>
                </>
              ))}
            </span>
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
