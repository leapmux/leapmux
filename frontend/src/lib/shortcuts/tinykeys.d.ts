declare module 'tinykeys' {
  export interface KeyBindingMap {
    [keybinding: string]: (event: KeyboardEvent) => void
  }

  export interface KeyBindingHandlerOptions {
    event?: 'keydown' | 'keyup'
    capture?: boolean
    timeout?: number
  }

  export function tinykeys(
    target: Window | HTMLElement,
    keyBindingMap: KeyBindingMap,
    options?: KeyBindingHandlerOptions,
  ): () => void

  export function createKeybindingsHandler(
    keyBindingMap: KeyBindingMap,
    options?: KeyBindingHandlerOptions,
  ): (event: KeyboardEvent) => void

  export function parseKeybinding(keybinding: string): Array<Array<string>>
}
