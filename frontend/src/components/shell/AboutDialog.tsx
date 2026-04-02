import type { Component } from 'solid-js'
import type { BuildInfo } from '~/lib/systemInfo'
import { Show } from 'solid-js'
import { Dialog } from '~/components/common/Dialog'
import { getBackendBuildInfo, getFrontendBuildInfo } from '~/lib/systemInfo'
import * as styles from './AboutDialog.css'

function formatBuildTime(iso: string): string {
  if (!iso)
    return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime()))
    return iso
  return d.toLocaleString(undefined, {
    weekday: 'short',
    year: 'numeric',
    month: 'numeric',
    day: 'numeric',
    hour: 'numeric',
    minute: '2-digit',
    second: '2-digit',
  })
}

function formatVersionLine(info: BuildInfo): string {
  let line = info.version || 'dev'
  if (info.commitHash)
    line += ` (${info.commitHash})`
  const time = formatBuildTime(info.buildTime)
  if (time)
    line += ` \u00B7 ${time}`
  return line
}

function buildInfoEquals(a: BuildInfo, b: BuildInfo): boolean {
  return a.version === b.version && a.commitHash === b.commitHash && a.buildTime === b.buildTime
}

interface AboutDialogProps {
  onClose: () => void
}

export const AboutDialog: Component<AboutDialogProps> = (props) => {
  const backend = getBackendBuildInfo()
  const frontend = getFrontendBuildInfo()
  const same = buildInfoEquals(backend, frontend)

  return (
    <Dialog title="About" onClose={props.onClose}>
      <section>
        <div class={styles.container}>
          <a class={styles.appName} href="https://github.com/leapmux/leapmux" target="_blank" rel="noopener noreferrer">
            LeapMux
          </a>
          <Show
            when={!same}
            fallback={<span class={styles.versionLine}>{formatVersionLine(backend)}</span>}
          >
            <div>
              <div class={styles.versionLabel}>Backend</div>
              <span class={styles.versionLine}>{formatVersionLine(backend)}</span>
            </div>
            <div>
              <div class={styles.versionLabel}>Frontend</div>
              <span class={styles.versionLine}>{formatVersionLine(frontend)}</span>
            </div>
          </Show>
          <span class={styles.copyright}>Copyright &copy; Event Loop, Inc.</span>
        </div>
      </section>
    </Dialog>
  )
}
