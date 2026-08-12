import { fireEvent } from '@solidjs/testing-library'
import { vi } from 'vitest'
import { HIDE_DELAY_MS, SHOW_DELAY_MS } from '~/components/common/Tooltip'

/**
 * Helpers that make a label report itself as clipped, or as fitting, under
 * jsdom.
 *
 * `Tooltip`'s clip detection reads live layout, and jsdom measures every box as
 * 0. It also loads no vanilla-extract rule, so the `overflow: hidden` that
 * `clippedText` declares is invisible to `getComputedStyle`. Each helper below
 * therefore states all three inputs that `isTargetClipped` reads: the rect,
 * `scrollWidth`, and `clientWidth`. The overflow longhands go on the element as
 * an INLINE style, and BOTH are set, because jsdom does not expand the
 * `overflow` shorthand.
 */

/** A zero-filled `DOMRect` with `rect` written over it. */
export function stubRect(el: Element, rect: Partial<DOMRect>): void {
  const base: DOMRect = {
    x: 0,
    y: 0,
    top: 0,
    left: 0,
    right: 0,
    bottom: 0,
    width: 0,
    height: 0,
    toJSON: () => '',
  }
  Object.defineProperty(el, 'getBoundingClientRect', {
    value: () => ({ ...base, ...rect }),
    configurable: true,
  })
}

/** The rect both helpers below use: on screen, and well inside the viewport. */
const ON_SCREEN: Partial<DOMRect> = { top: 10, left: 10, right: 70, bottom: 30, width: 60, height: 20 }

/** Makes the label report clipped text, so `showWhen: 'clipped'` lets it through. */
export function stubClipped(el: HTMLElement): void {
  el.setAttribute('style', 'overflow-x: hidden; overflow-y: hidden')
  stubRect(el, ON_SCREEN)
  Object.defineProperty(el, 'scrollWidth', { value: 400, configurable: true })
  Object.defineProperty(el, 'clientWidth', { value: 60, configurable: true })
}

/** Makes the label report that its text fits. */
export function stubFitting(el: HTMLElement): void {
  stubRect(el, ON_SCREEN)
  Object.defineProperty(el, 'scrollWidth', { value: 60, configurable: true })
  Object.defineProperty(el, 'clientWidth', { value: 60, configurable: true })
}

/**
 * Hovers the element, runs out the show delay, and gives the tooltip.
 *
 * Needs `vi.useFakeTimers()`. Gives `null` when no tooltip appeared, which is
 * what a label that fits looks like under `showWhen: 'clipped'`.
 */
export function hoverForTooltip(el: Element): HTMLElement | null {
  fireEvent.mouseEnter(el)
  vi.advanceTimersByTime(SHOW_DELAY_MS)
  return document.querySelector<HTMLElement>('[role="tooltip"]')
}

/**
 * Leaves the element and runs out the hide delay.
 *
 * Call this between two hovers of the SAME element. A visible tooltip stays
 * visible, so a second `hoverForTooltip` would report the tooltip that the first
 * one opened and never test the new state.
 */
export function unhoverTooltip(el: Element): void {
  fireEvent.mouseLeave(el)
  vi.advanceTimersByTime(HIDE_DELAY_MS)
}
