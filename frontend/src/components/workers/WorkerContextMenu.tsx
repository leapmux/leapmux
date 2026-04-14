import type { Component } from 'solid-js'
import type { WorkerInfo } from '~/lib/workerInfoCache'
import MoreHorizontal from 'lucide-solid/icons/more-horizontal'
import { For, Show } from 'solid-js'
import { isTunnelAvailable } from '~/api/platformBridge'
import { RelativeTime } from '~/components/chat/RelativeTime'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { IconButton } from '~/components/common/IconButton'
import { showInfoToast } from '~/components/common/Toast'
import { menuTrigger } from '~/components/tree/sidebarActions.css'
import { prettifyJson } from '~/lib/jsonFormat'
import { isSoloMode } from '~/lib/systemInfo'
import { dangerMenuItem } from '~/styles/shared.css'
import * as styles from './workerContextMenu.css'

interface WorkerContextMenuProps {
  workerInfo: WorkerInfo | null
  isOwner: boolean
  hasTunnels: boolean
  onAddTunnel: () => void
  onDeleteAllTunnels: () => void
  onDeregister: () => void
}

interface InfoRow {
  label: string
  value: string
  kind: 'text' | 'relative_time'
}

export const WorkerContextMenu: Component<WorkerContextMenuProps> = (props) => {
  const infoRows = (): InfoRow[] | null => {
    const info = props.workerInfo
    if (!info)
      return null
    let versionText = info.version
    if (info.commitHash)
      versionText += ` (${info.commitHash})`
    const rows: InfoRow[] = [
      { label: 'Name:', value: info.name, kind: 'text' },
      { label: 'Version:', value: versionText, kind: 'text' },
    ]
    if (info.buildTime)
      rows.push({ label: 'Built at:', value: info.buildTime, kind: 'relative_time' })
    rows.push({ label: 'OS:', value: `${info.os} (${info.arch})`, kind: 'text' })
    return rows
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
            class={styles.infoButton}
            onClick={() => {
              const json = infoJson()
              if (json)
                navigator.clipboard.writeText(json)
              showInfoToast('Worker info copied to clipboard')
            }}
          >
            <span class={styles.infoGrid}>
              <For each={rows()}>
                {row => (
                  <>
                    <span>{row.label}</span>
                    <span>
                      {row.kind === 'relative_time'
                        ? (
                            <>
                              <RelativeTime timestamp={row.value} />
                              {' ago'}
                            </>
                          )
                        : row.value}
                    </span>
                  </>
                )}
              </For>
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
