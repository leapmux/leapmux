import type { Component } from 'solid-js'
import type { ControlPermissionPill } from './permissionPresets'

import type { PillOptions } from '~/components/common/PillGroup'
import { PillGroup } from '~/components/common/PillGroup'
import * as styles from '../ControlRequestBanner.css'

/**
 * The permission pill group a control request's decision row offers: Unchanged /
 * Smart / Bypass, one row segment shared by the decision footer and the
 * providers that lay out their own action row (ACP, OpenCode). The group's own
 * name supplies the noun each option drops.
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

/** One provider-derived choice for how the positive action grants access. */
export interface ControlAllowChoicePill {
  label: string
  description?: string
  options: PillOptions<string>
  selected: string
  onSelect: (key: string) => void
}

/**
 * The allow-choice pill group for a control request. ACP supplies duration
 * scopes. Codex supplies its native turn, session, and policy choices.
 */
export const ControlAllowChoicePillGroup: Component<{ pill: ControlAllowChoicePill }> = props => (
  <div class={styles.controlRequestPill} data-testid="control-allow-choice-pill-group">
    <PillGroup
      label={props.pill.label}
      description={props.pill.description}
      options={props.pill.options}
      selectedKey={props.pill.selected}
      onSelect={props.pill.onSelect}
      small
    />
  </div>
)
