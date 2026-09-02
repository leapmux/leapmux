import type { Component } from 'solid-js'
import type { CustomEditorComponent, CustomEditorId } from '../types'
import { AccountConnectedApps } from '../account/AccountConnectedApps'
import { AccountEmail } from '../account/AccountEmail'
import { AccountLinkedProviders } from '../account/AccountLinkedProviders'
import { AccountPasskeys } from '../account/AccountPasskeys'
import { AccountPassword } from '../account/AccountPassword'
import { AccountProfile } from '../account/AccountProfile'
import { AppRegistrations } from '../account/AppRegistrations'
import { KeybindingsControl } from './KeybindingsControl'
import { KeyPinsControl } from './KeyPinsControl'
import { NetworkAccessControl } from './NetworkAccessControl'
import { SyntaxThemeControl } from './SyntaxThemeControl'
import { TerminalThemeControl } from './TerminalThemeControl'
import { ThemeControl } from './ThemeControl'

/**
 * The administration twin of the account registrations panel: the same editor
 * with `variant="hub-wide"`, so a hub-wide registration is registered with the
 * form an administrator already knows instead of the CLI alone.
 *
 * A wrapper rather than a second registration form: the fields a hub-wide
 * registration states are the fields a private one states, and a copied form
 * is a second place a new editable field must reach.
 */
const HubWideAppRegistrations: Component = () => <AppRegistrations variant="hub-wide" />

/**
 * The ACCOUNT-side registrations panel, wrapped for the same reason its
 * hub-wide twin is: AppRegistrations states one optional prop, and a
 * whole-setting editor is handed the row's binding, which that prop type has
 * nothing in common with. The wrapper drops the binding this editor does not
 * read, rather than widening the panel's own props to accept it.
 */
const AccountAppRegistrations: Component = () => <AppRegistrations />

/**
 * The bespoke whole-setting editors a `{ kind: 'custom' }` control dispatches
 * to, keyed by the descriptor's customId. Every customId the proto registry
 * can produce must resolve here — `protoRegistry.test.ts` enforces that.
 */
export const CUSTOM_EDITORS: Record<CustomEditorId, CustomEditorComponent> = {
  keybindings: KeybindingsControl,
  /*
   * The ACCOUNT editors, one per row.
   *
   * They were one editor holding every account concern, with its own <h3>
   * headings inside a row whose label said something else — three label
   * styles on one panel, and "Command-line credentials" printed twice in two
   * of them. Each concern is now a row of its own, so the panel has ONE
   * vocabulary: the row supplies the label, the help and the separator, and
   * the editor supplies the control.
   *
   * Every one of them is hidden in solo mode by its registry entry's
   * `hidden`.
   */
  accountProfile: AccountProfile,
  accountEmail: AccountEmail,
  accountPassword: AccountPassword,
  accountPasskeys: AccountPasskeys,
  accountLinkedProviders: AccountLinkedProviders,
  /**
   * The account's connected apps — what it has authorized — with self-service
   * disconnection. It is the panel the credential-issued notice email links
   * to, and the one account row that needs NO elevated session: listing
   * carries metadata only, and disconnecting can only reduce access.
   */
  accountConnectedApps: AccountConnectedApps,
  /**
   * The account's app registrations — what it has registered for others to
   * authorize. Editing a redirect address on an existing registration diverts
   * an in-flight authorization code, so this row DOES demand an elevated
   * session, unlike its neighbour above.
   */
  accountAppRegistrations: AccountAppRegistrations,
  /**
   * The ADMINISTRATION-side registrations panel — the hub's own catalogue,
   * registering HUB_WIDE. Reachable only through the row PreferencesDialog
   * appends for administrators; the account side above never renders it.
   */
  hubWideAppRegistrations: HubWideAppRegistrations,
  keyPins: KeyPinsControl,
  /**
   * Which addresses this hub answers on, and the password that guards them.
   * Solo only -- the hub omits the descriptor elsewhere (hidden_in_hub), so
   * this entry is unreachable on a multi-user hub.
   */
  networkAccess: NetworkAccessControl,
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
