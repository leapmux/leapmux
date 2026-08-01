import type { Component } from 'solid-js'
import type { WorkerInfo } from '~/lib/workerInfoCache'
import { For, Show } from 'solid-js'
import { isTunnelAvailable } from '~/api/platformBridge'
import { RelativeTime } from '~/components/chat/RelativeTime'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { rowContextMenuTrigger } from '~/components/common/moreHorizontalTrigger'
import { showInfoToast } from '~/components/common/Toast'
import { copyTextToClipboard } from '~/lib/clipboard'
import { prettifyJson } from '~/lib/jsonFormat'
import { dangerMenuItem } from '~/styles/shared.css'
import * as styles from './workerContextMenu.css'

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

  // Routed through `copyTextToClipboard`, which is guarded: a non-secure origin
  // exposes no `navigator.clipboard` at all, so the bare
  // `navigator.clipboard.writeText` this replaced threw a TypeError into an
  // unhandled rejection. The toast is now conditional on the write actually
  // landing -- it used to fire even when there was no info to copy.
  const copyInfo = async () => {
    const json = infoJson()
    if (json && await copyTextToClipboard(json))
      showInfoToast('Worker info copied to clipboard')
  }

  return (
    <DropdownMenu trigger={rowContextMenuTrigger()}>
      <Show when={infoRows()}>
        {rows => (
          <button
            role="menuitem"
            class={styles.infoButton}
            onClick={() => void copyInfo()}
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
