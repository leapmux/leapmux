import type { JSX } from 'solid-js'
import type { ChatImageDeps, ChatImageResolution } from './chatImageResolve'
import type { ZoomMode } from '~/components/fileviewer/ImageToolbar'
import { createEffect, createMemo, createSignal, Match, onCleanup, Switch } from 'solid-js'
import * as styles from '~/components/fileviewer/FileViewer.css'
import { ImageRender } from '~/components/fileviewer/ImageFileView'
import { base64ToUint8Array } from '~/lib/base64'
import { resolveChatImage } from './chatImageResolve'

/**
 * The body of an IMAGE tab: one image an agent returned, resolved from the
 * message it arrived in.
 *
 * The tab stores only `(agentId, seq, imageIndex)`, so this component does the
 * finding -- from the chat store's loaded window when the message is still
 * there, else with one `GetAgentMessage` over E2EE. That indirection is the
 * point: the pixels never enter tab state, so the hub never sees them and no
 * screenshot rides along in a layer that is swept and mirrored for other
 * reasons.
 *
 * Rendering reuses `ImageRender` -- the same pane, toolbar and pinch-zoom the
 * file viewer uses for an image on disk.
 */
export function ChatImageViewer(props: {
  workerId: string
  agentId: string
  seq: bigint
  imageIndex: number
  /** Display name for the alt text; the tab strip shows the same title. */
  title?: string
  deps: ChatImageDeps
}): JSX.Element {
  const [resolution, setResolution] = createSignal<ChatImageResolution>({ status: 'pending' })
  const [zoom, setZoom] = createSignal<ZoomMode>('fit')

  createEffect(() => {
    const ref = {
      workerId: props.workerId,
      agentId: props.agentId,
      seq: props.seq,
      imageIndex: props.imageIndex,
    }
    let live = true
    onCleanup(() => {
      live = false
    })
    setResolution({ status: 'pending' })
    // A resolution that lands after the props moved on belongs to a reference
    // nobody is looking at; writing it would show the previous tab's image.
    void resolveChatImage(ref, props.deps).then((next) => {
      if (live)
        setResolution(next)
    })
  })

  // A memo, not a plain accessor: an image is megabytes, and base64-decoding it
  // again on any unrelated reactive tick in this component would be the most
  // expensive thing on the screen. It recomputes only when the resolution does.
  const bytes = createMemo(() => {
    const r = resolution()
    return r.status === 'ready' ? decodeImageBytes(r.source) : null
  })

  return (
    <Switch fallback={<div class={styles.loadingState}>Loading image…</div>}>
      <Match when={bytes()}>
        {decoded => (
          <ImageRender
            content={decoded().content}
            mimeType={decoded().mimeType}
            name={props.title ?? 'Image'}
            zoom={zoom()}
            onZoomChange={setZoom}
          />
        )}
      </Match>
      <Match when={resolution().status === 'gone'}>
        <div class={styles.errorState}>
          This image is no longer in the conversation.
        </div>
      </Match>
      <Match when={resolution().status === 'error'}>
        <div class={styles.errorState}>
          {(resolution() as { status: 'error', message: string }).message}
        </div>
      </Match>
    </Switch>
  )
}

/**
 * Turn a resolved source into bytes the blob URL can hold.
 *
 * Only inline base64 is decodable here. An image the agent named by URL was
 * never inlined in the transcript either -- the chat row shows it as a link for
 * the same anti-exfiltration reason -- so there is nothing local to open, and
 * such an image never becomes a tab (see the click handler in ImageResultView).
 */
export function decodeImageBytes(source: { data?: string, url?: string, mimeType?: string }): { content: Uint8Array, mimeType: string } | null {
  const inline = source.data
    ? { base64: source.data, mimeType: source.mimeType }
    : parseDataUrl(source.url)
  if (!inline?.base64 || !inline.mimeType)
    return null
  try {
    return { content: base64ToUint8Array(inline.base64), mimeType: inline.mimeType }
  }
  catch {
    return null
  }
}

/** Split a `data:<mime>;base64,<payload>` URL. Returns null for anything else. */
function parseDataUrl(url: string | undefined): { base64: string, mimeType: string } | null {
  if (!url?.startsWith('data:'))
    return null
  const comma = url.indexOf(',')
  if (comma < 0)
    return null
  const meta = url.slice('data:'.length, comma)
  if (!meta.toLowerCase().endsWith(';base64'))
    return null
  return { base64: url.slice(comma + 1), mimeType: meta.slice(0, meta.length - ';base64'.length) }
}
