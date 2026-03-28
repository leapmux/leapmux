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
