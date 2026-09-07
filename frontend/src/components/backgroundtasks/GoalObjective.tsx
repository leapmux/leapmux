import type { Component } from 'solid-js'
import { createEffect, createSignal, on, onCleanup, onMount, Show } from 'solid-js'
import { MarkdownText } from '~/components/chat/messageRenderers'
import { Tooltip } from '~/components/common/Tooltip'
import { collapsibleToggle } from '~/styles/shared.css'
import * as styles from './GoalObjective.css'

export interface GoalObjectiveProps {
  /** The objective, as the provider stored it: markdown source. */
  objective: string
}

/**
 * A sub-pixel difference is not overflow. A box whose content rounds to one
 * pixel taller than its clamp hides nothing a reader can see, and offering a
 * `Show more` for it is noise. `Tooltip` uses the same tolerance for the same
 * measurement.
 */
const OVERFLOW_TOLERANCE_PX = 1

/**
 * The session goal's objective: rendered markdown, clamped to four lines, with
 * both routes back to the rest of it.
 *
 * One component rather than a style plus a caller, because a clamp HIDES text
 * and the routes back are what make that acceptable. `ClippedText` owns the
 * same pairing for a one-line label; this is its multi-line sibling.
 *
 * Two routes, for two different jobs. The hover tooltip is a peek that costs no
 * click and no layout. The disclosure is the complete read: it expands inside
 * the panel, which scrolls, so an objective of any length is reachable there --
 * and the tooltip, which takes no pointer events, could never scroll to it.
 *
 * A SINGLE newline in the objective is a space, not a line break. The card used
 * to set the objective `white-space: pre-wrap`, where it was a break. This is
 * CommonMark, and it is what every other rendered surface in the app does with
 * the same input, because `createMarkdownParser` uses `remarkParse` and
 * `remarkGfm` and no `remark-breaks`. A goal written in the dialog is
 * unaffected: the editor ends a paragraph with a blank line.
 */
export const GoalObjective: Component<GoalObjectiveProps> = (props) => {
  const [expanded, setExpanded] = createSignal(false)
  const [overflows, setOverflows] = createSignal(false)
  let boxEl: HTMLDivElement | undefined
  let contentEl: HTMLDivElement | undefined

  /**
   * Whether the clamped box hides anything.
   *
   * Measured ONLY while collapsed, and the answer persists while expanded. An
   * expanded box is as tall as its content and overflows by definition nothing,
   * so a live measurement there would answer "no" and take away the `Show less`
   * control that is the only way back to the clamped state.
   */
  const measure = () => {
    if (!boxEl || expanded())
      return
    setOverflows(boxEl.scrollHeight - boxEl.clientHeight > OVERFLOW_TOLERANCE_PX)
  }

  onMount(() => {
    if (!contentEl)
      return
    // The CONTENT, not the box. The box's height is pinned by the clamp, so it
    // never resizes when a longer objective arrives or when the asynchronous
    // markdown highlight lands and reflows it. The content element grows with
    // both, and it also tracks the box's width -- so a narrower sidebar, which
    // re-wraps the same text onto more lines, re-measures through the same
    // observer.
    const observer = new ResizeObserver(measure)
    observer.observe(contentEl)
    onCleanup(() => observer.disconnect())
  })

  // Re-measure whenever the clamp goes back on, because `measure` declines to
  // run while the box is open. Not deferred: the first run is the initial
  // measurement, for the frame the card mounts in.
  createEffect(on(expanded, measure))

  // A new objective starts collapsed. An expansion belongs to the text the
  // reader opened, not to the one that replaced it. The observer re-measures
  // for the new text; a replacement of the same height keeps the same answer.
  createEffect(on(() => props.objective, () => setExpanded(false), { defer: true }))

  return (
    <div class={styles.root}>
      <Tooltip
        // Built ONLY while the box hides something. `Tooltip` resolves
        // `content` in a memo, which is eager, so an unconditional element
        // would render the objective's markdown a second time for every card
        // that shows one -- and two cards can be on screen at once.
        //
        // No `text` beside it. `text` becomes an offscreen description, and the
        // objective is markdown SOURCE: a screen reader would read the asterisks
        // of every bold run. The rendered objective is already on screen, and
        // the disclosure below reaches the rest of it.
        content={overflows() && !expanded()
          ? (
              <div class={styles.tooltipBody}>
                <MarkdownText text={props.objective} />
              </div>
            )
          : undefined}
        showWhen="clipped"
      >
        <div
          ref={boxEl}
          class={styles.body}
          classList={{
            [styles.bodyClamped]: !expanded(),
            [styles.bodyFaded]: !expanded() && overflows(),
          }}
          data-testid="goal-objective"
        >
          {/* The measured element. It exists so the ResizeObserver has
              something that actually changes size -- see `onMount`. */}
          <div ref={contentEl}>
            <MarkdownText text={props.objective} />
          </div>
        </div>
      </Tooltip>
      <Show when={overflows()}>
        <div class={styles.toggleRow}>
          <button
            type="button"
            class={collapsibleToggle}
            data-testid="goal-objective-toggle"
            onClick={() => setExpanded(prev => !prev)}
          >
            {expanded() ? 'Show less' : 'Show more'}
          </button>
        </div>
      </Show>
    </div>
  )
}
