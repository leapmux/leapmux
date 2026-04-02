import type { Component } from 'solid-js'
import type { BuildInfo } from '~/lib/systemInfo'
import { Show } from 'solid-js'
import { Dialog } from '~/components/common/Dialog'
import { getBackendBuildInfo, getFrontendBuildInfo } from '~/lib/systemInfo'
import { labelRow } from '~/styles/shared.css'

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

interface AboutDialogProps {
  onClose: () => void
}

export const AboutDialog: Component<AboutDialogProps> = (props) => {
  const backend = getBackendBuildInfo()
  const frontend = getFrontendBuildInfo()
  const same = formatVersionLine(backend) === formatVersionLine(frontend)

  return (
    <Dialog title="About" onClose={props.onClose}>
      <section>
        <div class="vstack gap-4">
          <div>
            <strong>LeapMux</strong>
            {' - AI Coding Agent Multiplexer'}
          </div>
          <Show
            when={!same}
            fallback={(
              <div>
                <div class={labelRow}>Version</div>
                {formatVersionLine(backend)}
              </div>
            )}
          >
            <div>
              <div class={labelRow}>Backend</div>
              {formatVersionLine(backend)}
            </div>
            <div>
              <div class={labelRow}>Frontend</div>
              {formatVersionLine(frontend)}
            </div>
          </Show>
          <div>
            <div class={labelRow}>Homepage</div>
            <a href="https://github.com/leapmux/leapmux" target="_blank" rel="noopener noreferrer">
              github.com/leapmux/leapmux
            </a>
          </div>
          <div>
            <div class={labelRow}>License</div>
            <a href="https://github.com/leapmux/leapmux/blob/main/LICENSE.md" target="_blank" rel="noopener noreferrer">
              Functional Source License, Version 1.1, ALv2 Future License
            </a>
          </div>
          <div>
            <div class={labelRow}>Third-party licenses</div>
            <a href="https://github.com/leapmux/leapmux/blob/main/NOTICE.md" target="_blank" rel="noopener noreferrer">
              NOTICE.md
            </a>
          </div>
          <div>Copyright &copy; Event Loop, Inc.</div>
          <div>
            <small>
              All product names, logos, and trademarks are the property of their respective owners.
              LeapMux is not affiliated with, endorsed by, or sponsored by any third party.
              Agent icons are used solely to indicate compatibility and are reproduced for identification purposes only.
            </small>
          </div>
        </div>
      </section>
    </Dialog>
  )
}
