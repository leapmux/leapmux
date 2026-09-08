import type { Component } from 'solid-js'
import { createEffect, createSignal, createUniqueId, on, onCleanup, onMount, Show } from 'solid-js'
import { MarkdownText } from '~/components/chat/messageRenderers'
import { CollapsibleToggle } from '~/components/common/CollapsibleToggle'
import { Tooltip } from '~/components/common/Tooltip'
import * as styles from './GoalObjective.css'

export interface GoalObjectiveProps {
  /** The objective, as the provider stored it: markdown source. */
  objective: string
}

/**
 * A sub-pixel difference is not overflow. A box whose content rounds to one
 * pixel taller than its clamp hides nothing a reader can see, and offering a
 * `Show more` for it is noise.
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
 *
 * A PROVIDER can still send soft-wrapped plain text, and it reads as one
 * paragraph here. Claude folds an objective to a single line before it stores
 * one, but Codex, ZCode and Reasonix copy the raw string through. The case is
 * known and accepted: one card cannot answer this input differently from every
 * other markdown surface without splitting the app's single parser config, and
 * `remark-breaks` is an app-wide decision rather than a goal-card one.
 */
export const GoalObjective: Component<GoalObjectiveProps> = (props) => {
  const [expanded, setExpanded] = createSignal(false)
  // The clamped box, so the disclosure can point at what it opens.
  const bodyId = createUniqueId()
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
        // No `showWhen="clipped"`. This component ALREADY knows whether the box
        // hides anything -- it measures that itself, and `content` below is
        // gated on the answer -- so asking `Tooltip` to measure the same box
        // again gives one fact two detectors, with two tolerances and two
        // algorithms. They agree by construction today, and the second one
        // reads live layout, so the two disagree in the window where this
        // component's own answer is still stale.
        content={overflows() && !expanded()
          ? (
              <div class={styles.tooltipBody}>
                <MarkdownText text={props.objective} />
              </div>
            )
          : undefined}
      >
        <div
          ref={boxEl}
          class={styles.body}
          classList={{
            [styles.bodyClamped]: !expanded(),
            [styles.bodyFaded]: !expanded() && overflows(),
          }}
          data-testid="goal-objective"
          id={bodyId}
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
          <CollapsibleToggle
            expanded={expanded()}
            onToggle={() => setExpanded(prev => !prev)}
            controls={bodyId}
            moreLabel="Show more"
            data-testid="goal-objective-toggle"
          />
        </div>
      </Show>
    </div>
  )
}
