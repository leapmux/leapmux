import type { CustomEditorComponent, CustomEditorId } from '../types'
import { ProfileSettings } from '../ProfileSettings'
import { KeybindingsControl } from './KeybindingsControl'
import { KeyPinsControl } from './KeyPinsControl'
import { SyntaxThemeControl } from './SyntaxThemeControl'
import { TerminalThemeControl } from './TerminalThemeControl'
import { ThemeControl } from './ThemeControl'

/**
 * The bespoke whole-setting editors a `{ kind: 'custom' }` control dispatches
 * to, keyed by the descriptor's customId. Every customId the proto registry
 * can produce must resolve here — `protoRegistry.test.ts` enforces that.
 */
export const CUSTOM_EDITORS: Record<CustomEditorId, CustomEditorComponent> = {
  keybindings: KeybindingsControl,
  /**
   * The account rows: the existing ProfileSettings sections (username,
   * display name, email, password, linked accounts), unchanged. Hidden in
   * solo mode by its registry entry's `hidden`.
   */
  account: ProfileSettings,
  keyPins: KeyPinsControl,
  /**
   * The palette drop-down and the light/dark tri-switch, as one control.
   * `theme` is one key holding `{name, mode}`, so one row carries one scope
   * chip and one Reset for the whole appearance choice.
   */
  theme: ThemeControl,
  /**
   * The terminal's own palette and mode, each able to say "Match UI". Separate
   * from `theme` so the two surfaces can differ; see TerminalThemeControl.
   */
  terminalTheme: TerminalThemeControl,
  /** Highlighted code. Separate from the terminal: different surface, different habits. */
  syntaxTheme: SyntaxThemeControl,
}
