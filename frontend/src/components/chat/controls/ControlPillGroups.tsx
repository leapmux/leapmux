import type { Component } from 'solid-js'
import type { ControlPermissionPill } from './permissionPresets'

import type { PillOptions } from '~/components/common/PillGroup'
import { PillGroup } from '~/components/common/PillGroup'
import { Tooltip } from '~/components/common/Tooltip'
import * as styles from '../ControlRequestBanner.css'

/**
 * The permission pill group a control request's decision row offers: Default /
 * Smart permissions / Bypass permissions, one row segment shared by the decision
 * footer and the providers that lay out their own action row (ACP, OpenCode).
 */
export const ControlPermissionPillGroup: Component<{ pill: ControlPermissionPill }> = props => (
  <Tooltip text="The selected preset applies when you allow or approve this request">
    <div class={styles.controlRequestPill} data-testid="control-permissions-pill-group">
      <PillGroup
        label="Permissions"
        options={props.pill.options}
        selectedKey={props.pill.selected}
        onSelect={props.pill.onSelect}
      />
    </div>
  </Tooltip>
)

/**
 * The allow-scope pill group a permission request offers when one allow-once
 * faces one or more allow-always scopes (Once / Always, or Once / Session /
 * Project): the scope control for HOW LONG an allow lasts, keys being optionIds
 * so a selection maps straight onto the wire reply.
 */
export const ControlAllowScopePillGroup: Component<{
  options: PillOptions<string>
  selected: string
  onSelect: (optionId: string) => void
}> = props => (
  <div class={styles.controlRequestPill} data-testid="control-allow-scope-pill-group">
    <PillGroup
      label="Allow scope"
      options={props.options}
      selectedKey={props.selected}
      onSelect={props.onSelect}
    />
  </div>
)
