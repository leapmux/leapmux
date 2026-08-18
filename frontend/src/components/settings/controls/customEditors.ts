import type { CustomEditorComponent, CustomEditorId } from '../types'
import { ProfileSettings } from '../ProfileSettings'
import { KeybindingsControl } from './KeybindingsControl'
import { KeyPinsControl } from './KeyPinsControl'

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
}
