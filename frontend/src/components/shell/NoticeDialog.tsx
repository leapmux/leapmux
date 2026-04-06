import type { Component } from 'solid-js'
import { createResource, Show } from 'solid-js'
import { markdownContent } from '~/components/chat/markdownContent.css'
import { Dialog } from '~/components/common/Dialog'
import { renderMarkdown } from '~/lib/renderMarkdown'

interface NoticeDialogProps {
  onClose: () => void
}

async function fetchNotice(): Promise<string> {
  const res = await fetch('/NOTICE.md')
  if (!res.ok)
    throw new Error(`Failed to fetch NOTICE.md: ${res.status}`)
  return res.text()
}

export const NoticeDialog: Component<NoticeDialogProps> = (props) => {
  const [notice] = createResource(fetchNotice)

  return (
    <Dialog title="Third-Party Licenses" tall wide onClose={props.onClose}>
      <section>
        <Show when={!notice.loading} fallback={<div>Loading…</div>}>
          <Show when={notice.error}>
            <div>Failed to load third-party licenses.</div>
          </Show>
          <Show when={notice()}>
            {/* eslint-disable-next-line solid/no-innerhtml -- intentional: rendered markdown */}
            <div class={markdownContent} innerHTML={renderMarkdown(notice()!, true)} />
          </Show>
        </Show>
      </section>
    </Dialog>
  )
}
