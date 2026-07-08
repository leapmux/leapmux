import type { DotCluster } from './chatRailPolicy'
import { Show } from 'solid-js'
import { MarkType } from '~/generated/leapmux/v1/agent_pb'
import * as styles from './ChatScrollRail.css'
import { MarkdownText } from './messageRenderers'

// ---------------------------------------------------------------------------
// Scroll-rail dot preview presentation
//
// The tooltip/label surface for a rail jump dot, split from ChatScrollRail so the component
// is left to own geometry + interaction. Pure presentation: the "which dot is active" and
// "warm its preview" wiring stays in the rail.
// ---------------------------------------------------------------------------

/** Fallback tooltip label for a mark whose content has no previewable text. */
function markLabel(type: number): string {
  return type === MarkType.CONTROL_RESPONSE ? 'Your response' : 'Your message'
}

/** "N messages" wording shared by a cluster dot's aria-label and its tooltip header. */
function clusterCountLabel(count: number): string {
  return `${count} messages`
}

/** Accessible label / tooltip fallback for a dot: a count for a cluster, else the mark type. */
export function dotLabel(cluster: DotCluster): string {
  return cluster.count > 1 ? clusterCountLabel(cluster.count) : markLabel(cluster.type)
}

/**
 * Tooltip body for a jump dot: an aggregate "N messages" header when the dot is a cluster,
 * then the representative's preview -- rendered markdown (the marked message's content is
 * markdown), a mark-type label when there's no previewable text, or a loading line while the
 * fetch is in flight. Reads `previewFor` reactively so a slow fetch that lands after the
 * tooltip opens still fills it in. The preview string is already length-bounded upstream
 * (truncatePreview), so rendered markdown stays small.
 */
export function DotPreview(props: { previewFor: () => string | undefined, markType: number, count: number }) {
  const preview = () => props.previewFor()
  return (
    <>
      <Show when={props.count > 1}>
        <div class={styles.dotPreviewCount}>{clusterCountLabel(props.count)}</div>
      </Show>
      <Show
        when={preview() !== undefined}
        fallback={<span class={styles.dotPreviewLoading}>Loading preview…</span>}
      >
        <Show when={preview()} fallback={<span>{markLabel(props.markType)}</span>}>
          <div class={styles.dotPreviewMarkdown}>
            <MarkdownText text={preview()!} />
          </div>
        </Show>
      </Show>
    </>
  )
}
