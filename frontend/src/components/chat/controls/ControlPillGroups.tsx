import type { Component } from 'solid-js'
import type { ControlPermissionPill } from './permissionPresets'

import type { PillOptions } from '~/components/common/pillOptions'
import { PillGroup } from '~/components/common/PillGroup'
import * as styles from '../ControlRequestBanner.css'

/**
 * Display the permission preset choices in the request's decision row.
 * The shared footer and provider control surfaces use this group.
 * Its accessible Permissions name identifies the Unchanged, Smart, and Bypass choices.
 */
export const ControlPermissionPillGroup: Component<{ pill: ControlPermissionPill }> = props => (
  <div class={styles.controlRequestPill} data-testid="control-permissions-pill-group">
    <PillGroup
      label="Permissions"
      description="The selected preset applies when you allow or approve this request"
      options={props.pill.options}
      selectedKey={props.pill.selected}
      onSelect={props.pill.onSelect}
      small
    />
  </div>
)

/**
 * The caller maps each selected key to its native positive reply.
 * Treat the key as opaque in this group.
 * Each native path supplies its own meaning:
 *
 * - ACP and OpenCode keys contain the offered optionId.
 * - Codex decision keys identify a local choice and must not reach the wire directly.
 * - Codex permission keys contain the turn or session scope.
 *
 * Require the caller's lookup before sending a key.
 * The caller also supplies the accessible group label that tests use for lookup.
 */
export interface ControlAllowChoicePill {
  label: string
  options: PillOptions<string>
  selected: string
  onSelect: (key: string) => void
}

/**
 * The allow-choice pill group for a control request. ACP supplies duration
 * scopes. Codex supplies its own turn, session, and policy decisions.
 */
export const ControlAllowChoicePillGroup: Component<{ pill: ControlAllowChoicePill }> = props => (
  <div class={styles.controlRequestPill} data-testid="control-allow-choice-pill-group">
    <PillGroup
      label={props.pill.label}
      options={props.pill.options}
      selectedKey={props.pill.selected}
      onSelect={props.pill.onSelect}
      small
    />
  </div>
)
