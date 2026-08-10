import type { DocStats } from './editorSetup'
import { createComputed, createSignal, onCleanup } from 'solid-js'

/**
 * The right padding the collapsed layout falls back to before the action row is
 * measured. The stylesheet declares the same number as the fallback of
 * `--composer-right-pad`, and both exist only for the frames before the first
 * ResizeObserver callback.
 */
const UNMEASURED_RIGHT_PAD_PX = 96

/**
 * Expand as soon as the text would wrap at the narrow width, and collapse only
 * once it clears the threshold by a wider margin. The asymmetry is hysteresis:
 * the layout switch itself changes the measured width, so equal thresholds let
 * a borderline string oscillate.
 */
const EXPAND_MARGIN_PX = 16
const COLLAPSE_MARGIN_PX = 32

/** The DOM the layout measures. Each getter is a ref that resolves after mount. */
export interface ComposerLayoutRefs {
  /** The editor wrapper. Hosts the width probe, so the probe inherits its font. */
  editorRoot: () => HTMLElement | undefined
  /**
   * The document's first RENDERED block, whose inline content the probe clones.
   *
   * Safe to read at measurement time: the notifier fires from ProseMirror's
   * `view().update` hook, which runs after `docView.update()` has already
   * written the new content to the DOM.
   */
  firstBlock: () => Element | null | undefined
  /** The editor row. Its width and left padding bound the collapsed text area. */
  row: () => HTMLElement | undefined
  /** The action-row slot. Its width and height size the collapsed padding and the reservation. */
  actionSlot: () => HTMLElement | undefined
}

export interface ComposerLayout {
  /** Whether the content needs the expanded layout. */
  contentExpanded: () => boolean
  /** The collapsed-mode right padding, in pixels. */
  rightPad: () => number
  /** The action row's measured height in pixels, or 0 before the first measurement. */
  actionsHeight: () => number
  /** Feed the classified document. Called from the ProseMirror transaction hook. */
  setDocStats: (stats: DocStats) => void
  /**
   * Start measuring. Call once from `onMount`, after the refs resolve. Registers
   * its own cleanup on the calling owner, so call it from a LIVE owner — never
   * after an `await`, where the component may already have been disposed.
   */
  observe: () => void
}

/**
 * The composer box's expand/collapse decision, and the measurements it needs.
 *
 * Both EXPAND and COLLAPSE are width-based, never height-based. The layout
 * switch changes the text area's width (expanded is wider — it drops the
 * overlay side padding), so a height check is unstable: borderline text that
 * fits one line at the wide width would collapse, re-narrow the area, re-wrap
 * to two lines, and loop. Instead the text's natural unwrapped width is
 * compared against the available width at the NARROW layout, a value the mode
 * flip does not change.
 *
 * Everything the decision reads lives here: the three measurements, both hidden
 * probe elements, all three ResizeObservers, and their disposal. That is the
 * point of the unit — a fourth measurement cannot end up with its observer torn
 * down in a different place from the other three, and the decision itself is
 * reachable from a test without mounting Milkdown.
 */
export function createComposerLayout(refs: ComposerLayoutRefs): ComposerLayout {
  const [docStats, setDocStats] = createSignal<DocStats>({ multiLine: false, text: '' })
  const [rightPad, setRightPad] = createSignal(UNMEASURED_RIGHT_PAD_PX)
  const [actionsHeight, setActionsHeight] = createSignal(0)
  // Zero means "not measured yet", which reads as unlimited width below, so the
  // editor starts collapsed instead of flashing expanded before the first
  // measurement lands.
  const [rowWidth, setRowWidth] = createSignal(0)
  const [collapsedLeftPad, setCollapsedLeftPad] = createSignal(0)
  const [contentExpanded, setContentExpanded] = createSignal(false)

  /**
   * Measure the natural (unwrapped) width of the document's first block with a
   * throwaway element.
   *
   * The probe clones the block's RENDERED inline content rather than carrying
   * its plain text. Plain text measured every mark as unstyled: a bold run or a
   * monospace inline-code span renders wider than the same characters in the
   * body font, and an inline atom contributes no text at all — so a paragraph
   * could measure comfortably inside the threshold and still wrap on screen,
   * putting its second line under the overlaid buttons. The clone carries the
   * marks and the atoms, so the browser measures what it will actually draw.
   *
   * The probe inherits the editor's font from its parent and adds only
   * `white-space: pre`, so the measurement is the unwrapped width.
   *
   * `fallbackText` covers the frames before ProseMirror has rendered anything
   * (the seed from a loaded draft), where there is no block to clone yet.
   */
  let probeEl: HTMLSpanElement | undefined
  const measureContentWidth = (fallbackText: string): number => {
    const block = refs.firstBlock()
    if (!block && !fallbackText)
      return 0
    if (!probeEl) {
      const root = refs.editorRoot()
      if (!root)
        return 0
      probeEl = document.createElement('span')
      probeEl.style.cssText = 'position:absolute;visibility:hidden;white-space:pre;max-width:none;left:0;top:0;'
      // Inside the editor wrapper, so it inherits the same font stack.
      root.appendChild(probeEl)
    }
    if (block) {
      // `computeDocStats` classifies any document holding a newline as
      // multi-line and the caller returns before measuring, so this block is
      // always one line: one probe write and one layout read.
      probeEl.replaceChildren(...Array.from(block.childNodes, node => node.cloneNode(true)))
    }
    else {
      probeEl.textContent = fallbackText
    }
    return probeEl.offsetWidth
  }

  /**
   * The available text width in the collapsed layout: the row's full width minus
   * the collapsed-mode ProseMirror paddings (the `[+]` button area on the left,
   * the action cluster on the right).
   *
   * Reads only signals, so the decision below re-runs on a resize as well as on
   * a document change.
   */
  const collapsedAvailableWidth = (): number => {
    const width = rowWidth()
    if (width === 0)
      return Infinity
    return width - collapsedLeftPad() - rightPad()
  }

  // createComputed, not createEffect, so the decision runs synchronously in the
  // same microtask as the document change -- BEFORE the browser paints.
  // createEffect defers past paint, which shows a two-line flash before the
  // expanded layout takes over.
  createComputed(() => {
    const stats = docStats()
    // Multi-line or non-paragraph content always expands, whatever its width.
    // Return before measuring: the width cannot change the answer, and measuring
    // forces a synchronous layout.
    if (stats.multiLine) {
      setContentExpanded(true)
      return
    }
    const textWidth = measureContentWidth(stats.text)
    const availWidth = collapsedAvailableWidth()
    setContentExpanded(prev => textWidth > availWidth - (prev ? COLLAPSE_MARGIN_PX : EXPAND_MARGIN_PX))
  })

  const observe = () => {
    const slot = refs.actionSlot()
    if (slot) {
      // A ResizeObserver catches the Interrupt button appearing and
      // disappearing (agent working/idle) and a control request swapping the
      // whole row in, so the padding and the reservation follow whatever
      // actually rendered rather than a hardcoded size.
      const measureSlot = () => {
        const right = Number.parseFloat(getComputedStyle(slot).right) || 0
        setRightPad(slot.offsetWidth + right)
        setActionsHeight(slot.offsetHeight)
      }
      measureSlot()
      const observer = new ResizeObserver(measureSlot)
      observer.observe(slot)
      onCleanup(() => observer.disconnect())
    }

    const row = refs.row()
    if (row) {
      // A throwaway probe resolves the CSS custom property to pixels, because an
      // unregistered custom property computes to its token stream, not a length.
      const padProbeEl = document.createElement('div')
      padProbeEl.style.cssText = 'position:absolute;visibility:hidden;width:100%;padding-left:var(--composer-left-pad);'
      row.appendChild(padProbeEl)
      const measureRow = () => {
        setRowWidth(row.clientWidth)
        setCollapsedLeftPad(Number.parseFloat(getComputedStyle(padProbeEl).paddingLeft) || 0)
      }
      measureRow()
      // Coalesce to one measurement per frame. The observed row carries the
      // animated `padding-bottom` that the expand/collapse switch drives, and a
      // ResizeObserver fires on every frame of that transition -- so an
      // uncoalesced callback runs a `getComputedStyle` read dozens of times per
      // flip, inside the busiest window the composer has.
      let rafId = 0
      const observer = new ResizeObserver(() => {
        cancelAnimationFrame(rafId)
        rafId = requestAnimationFrame(measureRow)
      })
      observer.observe(row)
      onCleanup(() => {
        cancelAnimationFrame(rafId)
        observer.disconnect()
        padProbeEl.remove()
      })
    }

    // Symmetric with padProbeEl. A full unmount takes the probe along with the
    // editor root, but a teardown that keeps the root alive must not leave
    // probes behind.
    onCleanup(() => {
      probeEl?.remove()
      probeEl = undefined
    })
  }

  return { contentExpanded, rightPad, actionsHeight, setDocStats, observe }
}
