import type { Component } from 'solid-js'
import type { PlanPermission } from '../model/controlPrompt'
import type { ControlRequest } from '~/stores/control.store'
import { createMemo, For, Show } from 'solid-js'
import { pluralize } from '~/lib/plural'
import * as styles from '../ControlRequestBanner.css'
import { CollapsibleList } from './CollapsibleList'
import { canAnswerControlRequest } from './controlResponseState'

/** Keep plan approval details here. The transcript contains the full plan. */
export const PlanApprovalContent: Component<{ request: ControlRequest, permissions?: readonly PlanPermission[], details?: readonly string[] }> = (props) => {
  const groups = createMemo(() => {
    const byTool = new Map<string, string[]>()
    for (const permission of props.permissions ?? []) {
      const prompts = byTool.get(permission.tool)
      if (prompts)
        prompts.push(permission.prompt)
      else
        byTool.set(permission.tool, [permission.prompt])
    }
    return Array.from(byTool, ([tool, prompts]) => ({ tool, prompts }))
  })

  return (
    <>
      <div class={styles.controlBannerTitle}>{canAnswerControlRequest(props.request) ? 'Plan Ready for Review' : 'Plan approval'}</div>
      <Show when={groups().length}>
        <div>
          <strong>Requested permissions:</strong>
          <ul>
            <CollapsibleList
              items={groups()}
              maxVisible={3}
              moreLabel={n => `Show ${pluralize(n, 'more group')}\u2026`}
              renderItem={group => <li>{`${group.tool}: ${group.prompts.join(', ')}`}</li>}
            />
          </ul>
        </div>
      </Show>
      <Show when={!groups().length && canAnswerControlRequest(props.request)}>
        <div>The agent requests approval to proceed.</div>
      </Show>
      <For each={props.details}>
        {detail => <div class={styles.bannerDetail}>{detail}</div>}
      </For>
    </>
  )
}
