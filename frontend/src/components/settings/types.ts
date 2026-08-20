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
export const CUSTOM_EDITOR_IDS = ['keybindings', 'account', 'keyPins', 'theme', 'terminalTheme', 'syntaxTheme'] as const

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
 * hub row has no device tier. Naming the dual shape apart means a dual
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

export type CategoryId = 'appearance' | 'notifications' | 'chat' | 'terminal' | 'files' | 'shortcuts' | 'advanced' | 'account' | 'general' | 'signup' | 'email' | 'captcha' | 'rate-limits' | 'limits'

/** A component rendering the whole-setting editor for a CustomEditorId. */
export type CustomEditorComponent = Component<Record<string, never>>

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
