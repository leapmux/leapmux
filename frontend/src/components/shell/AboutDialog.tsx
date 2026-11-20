import type { Component } from 'solid-js'
import { Show } from 'solid-js'
import { isTauriApp, platformBridge } from '~/api/platformBridge'
import { Dialog } from '~/components/common/Dialog'
import { labelRow } from '~/components/common/Dialog.css'
import { createLogger } from '~/lib/logger'
import { formatVersionLine, getBackendBuildInfo, getFrontendBuildInfo } from '~/lib/systemInfo'

interface AboutDialogProps {
  onClose: () => void
}

const log = createLogger('AboutDialog')

function openNoticeInTauriWindow(event: MouseEvent) {
  event.preventDefault()
  platformBridge.openNoticeWindow().catch((error: unknown) => {
    log.warn('could not open third-party licenses', { error })
  })
}

export const AboutDialog: Component<AboutDialogProps> = (props) => {
  const backend = getBackendBuildInfo()
  const frontend = getFrontendBuildInfo()
  const same = formatVersionLine(backend) === formatVersionLine(frontend)
  const buildYear = backend.commitTime
    ? new Date(backend.commitTime).getFullYear()
    : new Date().getFullYear()

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
            <div class="vstack gap-1">
              <a href="https://leapmux.dev/" target="_blank" rel="noopener noreferrer">
                leapmux.dev
              </a>
              <a href="https://github.com/leapmux/leapmux" target="_blank" rel="noopener noreferrer">
                github.com/leapmux/leapmux
              </a>
            </div>
          </div>
          <div>
            <div class={labelRow}>License</div>
            <a href="https://leapmux.dev/docs/reference/legal/" target="_blank" rel="noopener noreferrer">
              Functional Source License, Version 1.1, ALv2 Future License
            </a>
          </div>
          <div>
            <div class={labelRow}>Third-party licenses</div>
            <a
              href="/NOTICE.html"
              target="_blank"
              rel="noopener noreferrer"
              onClick={isTauriApp()
                ? openNoticeInTauriWindow
                : undefined}
            >
              NOTICE.html
            </a>
          </div>
          <div>{`Copyright \u00A9 ${buildYear} Event Loop, Inc.`}</div>
          <div>
            <small>
              All product names, logos, and trademarks are the property of their respective owners.
              LeapMux is not affiliated with, endorsed by, or sponsored by any third party.
              Coding agent, editor, and IDE icons are used solely to indicate compatibility and are reproduced for identification purposes only.
            </small>
          </div>
        </div>
      </section>
    </Dialog>
  )
}
