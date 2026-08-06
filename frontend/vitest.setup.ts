import { createRoot } from 'solid-js'
import { afterEach, beforeEach } from 'vitest'
import { setCRDTBridge } from './src/lib/crdt'
import { installTestBridge } from './src/test-support/crdtBridge'
import '@testing-library/jest-dom/vitest'

// Install a default CRDT bridge before every test so the projection-
// driven layout / tab / floating-window stores have a workspace +
// root tile to render. Tests that need a different userId / workspaceId
// / rootTileId can override by calling installTestBridge() themselves
// inside the test body.
//
// The bridge's reactive signal is created inside a per-test
// `createRoot` so Solid's signal subscription tracking works
// properly. The afterEach disposes the root, releasing the signal +
// any memos that depended on it.
//
// The default bridge seeds a single-LEAF root tile keyed `main-tile`
// so legacy tests that hard-code that id still find a matching node
// in the projection.
let disposeBridgeRoot: (() => void) | null = null

beforeEach(() => {
  createRoot((dispose) => {
    disposeBridgeRoot = dispose
    installTestBridge({ userId: 'user-test', workspaceId: 'ws-test', rootTileId: 'main-tile' })
  })
  // Clear browser storage: several stores now seed themselves from sessionStorage
  // on construction (e.g. `createTabMetadataStore` reads `KEY_TAB_MRU` eagerly,
  // before any render can run), and ~20 test files build it transitively through
  // `createTestTabStores`. Without a global clear, state leaked by one test
  // hydrates into the next. Per-file `beforeEach` clears remain where a test
  // needs finer control, but this is the backstop that makes the eager-seed
  // stores safe to construct anywhere.
  sessionStorage.clear()
  localStorage.clear()
})

afterEach(() => {
  if (disposeBridgeRoot) {
    disposeBridgeRoot()
    disposeBridgeRoot = null
  }
  setCRDTBridge(null)
})

// Node.js 25+ exposes a broken localStorage/sessionStorage stub on globalThis
// (bare object with no Storage methods). Vitest's jsdom environment uses
// populateGlobal() which skips keys already present on globalThis, so jsdom's
// proper Storage never gets applied. Fix this by re-defining the properties
// as getters that delegate to the jsdom window's Storage implementations.
declare const jsdom: { window: Window & typeof globalThis } | undefined

if (typeof globalThis.localStorage?.getItem !== 'function' && typeof jsdom !== 'undefined') {
  Object.defineProperty(globalThis, 'localStorage', {
    get: () => jsdom.window.localStorage,
    configurable: true,
  })
}
if (typeof globalThis.sessionStorage?.getItem !== 'function' && typeof jsdom !== 'undefined') {
  Object.defineProperty(globalThis, 'sessionStorage', {
    get: () => jsdom.window.sessionStorage,
    configurable: true,
  })
}

// Stub HTMLCanvasElement.getContext() to suppress jsdom's
// "Not implemented" warning when the canvas npm package is not installed.
HTMLCanvasElement.prototype.getContext = (() => null) as typeof HTMLCanvasElement.prototype.getContext

// jsdom does not implement ResizeObserver; provide an inert stub so
// components that observe layout changes render without throwing.
if (typeof globalThis.ResizeObserver === 'undefined') {
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver
}

// Run requestAnimationFrame synchronously in tests. Production code uses
// rAF to coalesce per-frame work (e.g. resize-drag pointermove), but
// tests assert on the synchronous result of dispatched events. The
// asynchronous timing of jsdom's setTimeout-backed rAF would force every
// test to await a frame; running synchronously is simpler and equivalent
// in correctness terms (one event in → one rAF callback out, just
// without the wait).
globalThis.requestAnimationFrame = ((cb: FrameRequestCallback): number => {
  cb(performance.now())
  return 0
}) as typeof globalThis.requestAnimationFrame
globalThis.cancelAnimationFrame = (() => {}) as typeof globalThis.cancelAnimationFrame

// jsdom does not implement the HTML Popover API. Provide minimal stubs so
// components that toggle popovers (DropdownMenu, GridSizePopover) don't
// throw under test. The stubs flip a data-attribute and dispatch a
// `toggle` event so listeners that key off it still fire.
type PopoverProto = HTMLElement & {
  showPopover: () => void
  hidePopover: () => void
  togglePopover: () => boolean
}
if (typeof (HTMLElement.prototype as Partial<PopoverProto>).showPopover !== 'function') {
  const proto = HTMLElement.prototype as PopoverProto
  proto.showPopover = function showPopover(this: HTMLElement) {
    if (this.matches('[data-popover-open]'))
      return
    this.setAttribute('data-popover-open', '')
    this.dispatchEvent(new Event('toggle', { bubbles: true }))
  }
  proto.hidePopover = function hidePopover(this: HTMLElement) {
    if (!this.matches('[data-popover-open]'))
      return
    this.removeAttribute('data-popover-open')
    this.dispatchEvent(new Event('toggle', { bubbles: true }))
  }
  proto.togglePopover = function togglePopover(this: HTMLElement) {
    if (this.matches('[data-popover-open]')) {
      this.hidePopover()
      return false
    }
    this.showPopover()
    return true
  }
}

// jsdom doesn't recognize the :popover-open pseudo-class; intercept
// matches() to handle it via the data-attribute the stubs above set.
const originalMatches = HTMLElement.prototype.matches
// Cast because lib.dom declares `matches` as a set of overloads whose tag-name
// forms are TYPE PREDICATES (`selectors: K): this is HTMLElementTagNameMap[K]`),
// and a plain `(selector: string) => boolean` is not assignable to those. The
// runtime contract is unchanged -- the delegate below preserves it -- so the
// assertion is about the declaration's shape, not about behaviour.
HTMLElement.prototype.matches = function matches(this: HTMLElement, selector: string): boolean {
  if (selector === ':popover-open')
    return this.hasAttribute('data-popover-open')
  return originalMatches.call(this, selector)
} as typeof HTMLElement.prototype.matches

// jsdom doesn't implement the native <dialog> API; provide stubs so
// components that wrap <dialog> (Dialog, ConfirmDialog, CloseGridDialog)
// can mount in tests. Individual tests still override these as needed.
if (typeof HTMLDialogElement !== 'undefined' && !HTMLDialogElement.prototype.showModal) {
  HTMLDialogElement.prototype.showModal = function showModal(this: HTMLDialogElement) {
    this.setAttribute('open', '')
  }
  HTMLDialogElement.prototype.close = function close(this: HTMLDialogElement) {
    this.removeAttribute('open')
    this.dispatchEvent(new Event('close'))
  }
}
