import type { Component } from 'solid-js'
import type { LiveCompaction } from '~/stores/chatCompaction'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import { Show } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { spinner } from '~/styles/animations.css'
import { resultDivider } from '../messageStyles.css'
import { COMPACTING_LABEL } from '../notificationEntries'

/** Display a live compaction independently of the model turn. */
export const CompactionIndicator: Component<{ compaction?: LiveCompaction | undefined }> = props => (
  <Show when={props.compaction}>
    {compaction => (
      <div class={resultDivider} data-testid="compaction-indicator" role="status">
        <Icon icon={LoaderCircle} size="sm" class={spinner} />
        {` ${COMPACTING_LABEL}${compaction().trigger
          ? ` (${compaction().trigger})`
          : ''}`}
      </div>
    )}
  </Show>
)
