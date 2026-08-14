import type { Component } from 'solid-js'
import type { MenuInfoRow } from '~/components/common/MenuInfoRows'
import type { WorkerInfo } from '~/lib/workerInfoCache'
import { createMemo, Show } from 'solid-js'
import { isTunnelAvailable } from '~/api/platformBridge'
import { RelativeTimeAgo } from '~/components/chat/RelativeTime'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { MenuInfoButton } from '~/components/common/MenuInfoRows'
import { rowContextMenuTrigger } from '~/components/common/moreHorizontalTrigger'
import { prettifyJson } from '~/lib/jsonFormat'
import { dangerMenuItem } from '~/styles/shared.css'

interface WorkerContextMenuProps {
  workerInfo: WorkerInfo | null
  // True for the in-process worker the solo launcher auto-registers.
  // The deregister handler refuses these (it would just re-register on
  // next start), so the menu item would be a dead-end click.
  autoRegistered: boolean
  hasTunnels: boolean
  onAddTunnel: () => void
  onDeleteAllTunnels: () => void
  onDeregister: () => void
}

export const WorkerContextMenu: Component<WorkerContextMenuProps> = (props) => {
  // Builds `MenuInfoRow[]` directly. `MenuInfoRow.value` is already a
  // JSX.Element, so the timestamp row carries its own `RelativeTimeAgo` instead
  // of a `kind` discriminator that a second loop had to switch on.
  const infoRows = createMemo((): MenuInfoRow[] => {
    const info = props.workerInfo
    if (!info)
      return []
    let versionText = info.version
    if (info.commitHash)
      versionText += ` (${info.commitHash})`
    const rows: MenuInfoRow[] = [
      { label: 'Name:', value: info.name },
      { label: 'Version:', value: versionText },
    ]
    if (info.buildTime)
      rows.push({ label: 'Built at:', value: <RelativeTimeAgo timestamp={info.buildTime} /> })
    rows.push({ label: 'OS:', value: `${info.os} (${info.arch})` })
    return rows
  })

  // Built from `workerInfo`, never from the displayed rows: the copy carries
  // `homeDir`, which the menu does not show.
  const infoJson = () => {
    const info = props.workerInfo
    if (!info)
      return ''
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
    <DropdownMenu trigger={rowContextMenuTrigger()}>
      <MenuInfoButton
        rows={infoRows()}
        copyText={infoJson}
        toastMessage="Worker info copied to clipboard"
      />
      <Show when={isTunnelAvailable()}>
        <button role="menuitem" onClick={() => props.onAddTunnel()}>
          Add tunnel...
        </button>
        <Show when={props.hasTunnels}>
          <button role="menuitem" class={dangerMenuItem} onClick={() => props.onDeleteAllTunnels()}>
            Delete all tunnels...
          </button>
        </Show>
      </Show>
      <Show when={!props.autoRegistered}>
        <hr />
        <button role="menuitem" class={dangerMenuItem} onClick={() => props.onDeregister()}>
          Deregister...
        </button>
      </Show>
    </DropdownMenu>
  )
}
