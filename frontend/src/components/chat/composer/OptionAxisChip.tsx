import type { JSX } from 'solid-js'
import type { ProviderSettingChange } from '~/components/chat/providers/registry'
import type { AvailableOptionGroup } from '~/generated/leapmux/v1/agent_pb'
import ChevronDown from 'lucide-solid/icons/chevron-down'
import { Show } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { Tooltip } from '~/components/common/Tooltip'
import * as styles from './composer.css'
import { OptionGroupPopover } from './OptionGroupPopover'

/**
 * Props for a single-axis settings chip (Model / Effort / Mode / any group).
 */
export interface OptionAxisChipProps {
  /** Group id to render (e.g. `model`, `effort`, `permissionMode`). */
  groupId: string
  /** The full option-group catalog (used to resolve the group + its options). */
  optionGroups: AvailableOptionGroup[] | undefined
  /** Optimistic option-value map keyed by group id. */
  optionValues: Record<string, string>
  /** Dispatch a change for this axis. */
  onChange?: (change: ProviderSettingChange) => void
  /** Whether the composer accepts setting changes at all. */
  disabled?: boolean
  /**
   * Marks a chip that the status bar drops first when the composer is narrow.
   * The responsive stylesheet hooks the resulting attribute.
   */
  optional?: boolean
  /** data-testid prefix for the popover + trigger. */
  testIdPrefix?: string
}

/**
 * One settings axis as a self-contained chip: a small button showing the
 * resolved current value, opening a popover with the group's options.
 */
export function OptionAxisChip(props: OptionAxisChipProps): JSX.Element {
  return (
    <OptionGroupPopover
      groupId={props.groupId}
      optionGroups={props.optionGroups}
      optionValues={props.optionValues}
      onChange={props.onChange}
      disabled={props.disabled}
      popoverClass={styles.axisPopover}
      popoverTestId={props.testIdPrefix ? `${props.testIdPrefix}-popover` : undefined}
      // The `[+]` menu renders the same group and keeps the plain
      // `<groupId>-<value>` ids, so the chip's own items are namespaced.
      itemTestIdPrefix={props.testIdPrefix}
      trigger={(triggerProps, view) => (
        <Tooltip text={view.label}>
          <button
            class={styles.axisChip}
            data-testid={props.testIdPrefix ? `${props.testIdPrefix}-trigger` : undefined}
            data-chip-optional={props.optional ? '' : undefined}
            {...triggerProps}
          >
            {/* Fall back to the group label when no option resolves yet: an
                unlabelled chip is a bare chevron the user cannot identify. */}
            <span class={styles.axisChipLabel}>{view.currentLabel || view.label}</span>
            <Show when={view.mutable}>
              <Icon icon={ChevronDown} size="xs" />
            </Show>
          </button>
        </Tooltip>
      )}
    />
  )
}
