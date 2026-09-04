import type { Component } from 'solid-js'

/**
 * Where a setting's value lives. `dual` settings have a browser-tier
 * override over an account-tier default (the scope chip picks the tier the
 * control edits); `hub` rows edit hub-instance settings via the admin RPCs.
 */
export type SettingScope = 'browser' | 'account' | 'dual' | 'hub'

/**
 * The bespoke editors that render a whole setting beyond one scalar control.
 *
 * A runtime list, not a bare union, because a `customId` also arrives from
 * the hub as an untyped wire string. `controlForField` checks that string
 * against this list; `CUSTOM_EDITORS` is keyed by the derived type, so the
 * component table stays exhaustive at compile time. One list serves both,
 * so a new editor cannot be recognised on the wire without a component.
 */
export const CUSTOM_EDITOR_IDS = [
  'keybindings',
  'accountProfile',
  'accountEmail',
  'accountPassword',
  'accountPasskeys',
  'accountLinkedProviders',
  'accountConnectedApps',
  'accountAppRegistrations',
  'hubWideAppRegistrations',
  'keyPins',
  'networkAccess',
  'trustedProxies',
  'theme',
  'terminalTheme',
  'syntaxTheme',
] as const

export type CustomEditorId = typeof CUSTOM_EDITOR_IDS[number]

export function isCustomEditorId(id: string): id is CustomEditorId {
  return (CUSTOM_EDITOR_IDS as readonly string[]).includes(id)
}

export type SettingControl
  = | { kind: 'enum', options: { value: string, label: string, help?: string }[] }
    | { kind: 'toggle' }
    | { kind: 'slider', min: number, max: number, step: number, unit?: string }
    | { kind: 'number', min?: number, max?: number, step?: number, unit?: string }
    | { kind: 'text', placeholder?: string }
    /**
     * `isSet` is an ACCESSOR, not a value. A control is built while the row
     * set is, so reading a setting value here would make the row memo
     * depend on every value — see `controlForField`.
     */
    | { kind: 'secret', isSet: () => boolean }
    | { kind: 'stringList', addLabel: string }
    | { kind: 'custom', id: CustomEditorId }
  /**
   * A row whose control is one button performing an action (e.g. "Reset all
   * browser overrides"). Not a value: the binding's `set` runs the action.
   */
    | { kind: 'action', label: string, danger?: boolean }

export interface SettingDescriptor {
  id: string
  category: CategoryId
  label: string
  help?: string
  keywords?: string[]
  scope: SettingScope
  control: SettingControl
  restart?: boolean
  /**
   * Whether the hub refuses this row's writes on a session that did not
   * prove a factor recently.
   *
   * READ THROUGH `descriptorNeedsElevation`, never directly: `scope: 'hub'`
   * already answers yes for every hub row, so this flag carries the ACCOUNT
   * rows alone. Setting it on a hub descriptor as well restated a fact the
   * scope beside it already gave.
   *
   * It never DECIDES anything. The hub refuses an un-elevated write on its own
   * and the transport turns that refusal into a prompt and one retry, so a row
   * that forgets this flag still behaves correctly — it simply says less. Two
   * rows in the Account group genuinely do not need one (the profile name, and
   * the command-line credentials), so this cannot be derived from the category
   * either.
   */
  needsElevation?: boolean
  hidden?: () => boolean
}

/**
 * Whether a descriptor's row renders right now.
 *
 * One reading of `hidden` for every consumer — the panel, the navigation's
 * occupancy test, the restart badge, and the search index. Re-typing
 * `!(d.hidden?.() ?? false)` per call site is how a row becomes searchable
 * while the panel that the result jumps to does not show it.
 */
export function descriptorVisible(d: SettingDescriptor): boolean {
  return !(d.hidden?.() ?? false)
}

/**
 * Whether the hub refuses this row's writes on an un-elevated session.
 *
 * The SCOPE answers it for every hub row, because the hub requires the same
 * window for every settings write rather than for one key at a time -- see
 * AdminSettingsService.requireElevatedWriter. So a hub descriptor no longer
 * carries the flag, and a hub key added later gets the answer from its scope.
 *
 * The explicit flag stays as the other case, because four ACCOUNT rows need it
 * and their scope cannot say so: the password, the passkeys, the email address
 * and the linked providers are refused, while the profile name and the
 * command-line credentials are not.
 */
export function descriptorNeedsElevation(d: SettingDescriptor): boolean {
  return d.scope === 'hub' || d.needsElevation === true
}

export interface SettingBinding {
  value: () => unknown
  set: (v: unknown) => void | Promise<void>
  overridden?: () => boolean
  clearOverride?: () => void // 'dual'
  /**
   * Switch a dual row onto its browser tier, seeding the override with the
   * current resolved value so the control does not jump. The scope chip's
   * "Override on this device" action.
   */
  beginOverride?: () => void
  customized?: () => boolean
  reset?: () => Promise<void> // 'hub' | 'account'
  /**
   * The setting key `reset` clears, when clearing it takes MORE than this
   * row's own value with it.
   *
   * An object-shaped hub setting renders one row per field, but the reset
   * RPC removes the whole stored row — there is no per-field reset on the
   * wire. Unset, the reset affects exactly what the row shows and renders
   * as a plain button; set, the row must say what else goes and ask before
   * it goes. Resetting the SMTP host from an unconfirmed button also
   * destroyed the encrypted password, which the UI can never redisplay.
   */
  resetsWholeKey?: string
  /**
   * What the hub ENFORCES right now, and only while a read-time rule made
   * it differ from the configured value `value` carries. The row prints it
   * as the "currently in effect" note beside the control; it is never what
   * the control edits.
   */
  effective?: () => unknown
}

/**
 * The binding a DUAL-scope row supplies: every override member is
 * REQUIRED.
 *
 * `SettingBinding` makes them optional because one shape serves four
 * scopes, and a browser-only row has no account tier to address while a
 * hub row has no device tier. A separate type for the dual shape means a dual
 * factory that drops `clearOverride` or `customized` fails to compile,
 * rather than rendering a scope chip whose action does nothing.
 */
export interface DualBinding extends SettingBinding {
  overridden: () => boolean
  clearOverride: () => void
  beginOverride: () => void
  customized: () => boolean
  reset: () => Promise<void>
}

/**
 * The binding a HUB-scope row supplies: every hub row states whether it is
 * customized and can be reset.
 *
 * The sibling of `DualBinding`, for the same reason. `buildProtoRows` in
 * `./protoRegistry` is the factory this narrows. `resetsWholeKey` and
 * `effective` stay OPTIONAL: only an object-shaped key takes more than its
 * own row with it, and only a row a read-time rule overrides has a second
 * value to show.
 */
export interface HubBinding extends SettingBinding {
  customized: () => boolean
  reset: () => Promise<void>
}

/**
 * One row the dialog renders: what it looks like, and what it edits.
 *
 * The pair travels TOGETHER because neither half is useful alone, and the
 * two sources of rows — the browser registry and the hub's wire
 * descriptors — differ only in how they build it. Five memos used to
 * re-derive that split for themselves (the navigation's occupancy test,
 * the panel's two row lists, the restart marks, and the search index),
 * each branching on `group.admin` again, and the panel joined descriptor
 * to binding through an id-keyed record — a lookup with nothing to return
 * for an id the record does not hold.
 */
export interface SettingRowModel {
  descriptor: SettingDescriptor
  binding: SettingBinding
}

export type CategoryId = 'appearance' | 'notifications' | 'chat' | 'terminal' | 'desktop' | 'files' | 'shortcuts' | 'advanced' | 'account' | 'general' | 'signup' | 'email' | 'captcha' | 'rate-limits' | 'limits' | 'apps' | 'network'

/**
 * A component rendering the whole-setting editor for a CustomEditorId.
 *
 * It receives the row's BINDING, because an editor for a hub setting has to
 * read and write the setting: `binding.value()` is the stored document,
 * `binding.set(next)` writes it through the same store the rest of the panel
 * uses, and the row's own Customized/Reset chrome and error slot then stay
 * correct without a second write path.
 *
 * Most editors ignore it. The account ones own their whole value through their
 * own RPCs (`CUSTOM_EDITOR_OWNS_ITS_VALUE`), so their binding has nothing to
 * give them.
 */
export type CustomEditorComponent = Component<{ binding: SettingBinding }>

/**
 * The sentinel shape of a browser-stored preference: how "absent" maps to a
 * default and what "return to default" writes.
 *
 * - `nullable` — explicit value or absent; absent means "use the fallback
 *   (usually the account default)". Reset deletes the key.
 * - `default-on` — absent means true; an explicit `false` opts out. Reset
 *   deletes the key (absent = true again), never stores `true`.
 * - `default-off` — absent means false; only an explicit `true` opts in.
 *   Reset deletes the key.
 */
export type SentinelShape = 'nullable' | 'default-on' | 'default-off'
