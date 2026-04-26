import type { Component } from 'solid-js'
import { Show } from 'solid-js'
import { isTauriApp } from '~/api/platformBridge'
import { Dialog } from '~/components/common/Dialog'
import { labelRow } from '~/components/common/Dialog.css'
import { formatVersionLine, getBackendBuildInfo, getFrontendBuildInfo } from '~/lib/systemInfo'

interface AboutDialogProps {
  onClose: () => void
}

async function openNoticeInTauriWindow(e: MouseEvent) {
  e.preventDefault()
  const { WebviewWindow } = await import('@tauri-apps/api/webviewWindow')
  const win = new WebviewWindow('notice', {
    url: '/NOTICE.html',
    title: 'Third-party Licenses',
    width: 800,
    height: 600,
  })
  win.once('tauri://error', () => {
    // Window with this label may already exist; focus it instead.
    WebviewWindow.getByLabel('notice')?.then(w => w?.setFocus())
  })
}

export const AboutDialog: Component<AboutDialogProps> = (props) => {
  const backend = getBackendBuildInfo()
  const frontend = getFrontendBuildInfo()
  const same = formatVersionLine(backend) === formatVersionLine(frontend)
  const buildYear = backend.commitTime ? new Date(backend.commitTime).getFullYear() : new Date().getFullYear()

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
            <a href="/NOTICE.html" target="_blank" rel="noopener noreferrer" onClick={isTauriApp() ? openNoticeInTauriWindow : undefined}>
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
