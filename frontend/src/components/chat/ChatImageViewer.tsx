import type { JSX } from 'solid-js'
import type { ChatImageDeps, ChatImageResolution } from './chatImageResolve'
import type { ZoomMode } from '~/components/fileviewer/ImageToolbar'
import { createEffect, createMemo, createSignal, Match, on, onCleanup, Switch } from 'solid-js'
import * as styles from '~/components/fileviewer/FileViewer.css'
import { ImageRender } from '~/components/fileviewer/ImageFileView'
import { base64ToUint8Array } from '~/lib/base64'
import { imageRenderInfo, parseDataImageUrl } from '~/lib/imageBlocks'
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

  // `on()`, and the four props are the WHOLE dependency list. A bare effect
  // tracks whatever its body reads, and this body reads the chat store:
  // `resolveChatImage` is async, so it runs synchronously as far as the first
  // `await`, and when the message is already in the loaded window it reaches
  // none -- `getLoadedMessageBySeq` walks `messagesByAgent` inside the effect.
  // The effect then re-runs for every row the agent appends, and each re-run
  // resets the resolution to `pending`: `ImageRender` unmounts, its blob URL is
  // revoked, the pan is lost, and the image decodes again. `on()` runs the body
  // untracked, so only a new reference resolves again.
  createEffect(on(
    () => ({
      workerId: props.workerId,
      agentId: props.agentId,
      seq: props.seq,
      imageIndex: props.imageIndex,
    }),
    (ref) => {
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
    },
  ))

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
      {/* Resolved, and the bytes will not decode. `ImageResultView` draws a
          click target only for an image `imageRenderInfo` accepts, and this
          decoder now asks the same function, so a picture the row drew always
          decodes here.
          It stays reachable on purpose for the case the click cannot cover: the
          tab stores (agent, seq, index) rather than the bytes, so a message that
          merges in place can put a different source at index N between the click
          and the re-resolve. The alternative is the `fallback` below --
          "Loading image…" forever, on a tab that will never load. A sentence
          beats a spinner. */}
      <Match when={resolution().status === 'ready'}>
        <div class={styles.errorState}>
          This image cannot be displayed here.
        </div>
      </Match>
    </Switch>
  )
}

/**
 * Turn a resolved source into bytes the blob URL can hold.
 *
 * Only inline base64 is decodable here. An image the agent gave as a URL was
 * never inlined in the transcript either -- the chat row shows it as a link for
 * the same anti-exfiltration reason -- so there is nothing local to open, and
 * such an image never becomes a tab (see the click handler in ImageResultView).
 *
 * It applies the SAME `imageRenderInfo` policy the transcript row applies, from
 * the same module, so the tab and the row can never disagree about which
 * pictures are displayable. The tab addresses its image by (agent, seq, index)
 * and re-resolves it at open time, so the source it decodes is not necessarily
 * the one the click validated: a same-seq message merge can move index N.
 * Without the check here a 40 MB payload, or a type the row refuses, would
 * reach a Blob that the
 * row beside it refuses to draw.
 */
export function decodeImageBytes(source: { data?: string, url?: string, mimeType?: string }): { content: Uint8Array, mimeType: string } | null {
  const info = imageRenderInfo(source)
  if (!info.src)
    return null
  const inline = parseDataImageUrl(info.src)
  if (!inline)
    return null
  try {
    return { content: base64ToUint8Array(inline.base64), mimeType: inline.mimeType }
  }
  catch {
    return null
  }
}
