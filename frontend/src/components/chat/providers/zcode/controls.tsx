import type { Component } from 'solid-js'
import type { ActionsProps, ContentProps } from '../../controls/types'
import { createMemo, Match, Show, Switch } from 'solid-js'
import { ZCODE_TOOL } from '~/generated/contracts/zcode-protocol'
import { pickObject, pickString } from '~/lib/jsonPick'
import { getToolName } from '~/utils/controlResponse'
import * as styles from '../../ControlRequestBanner.css'
import { ExitPlanModeActions } from '../../controls/ExitPlanModeControl'
import { GenericToolActions, GenericToolContent } from '../../controls/GenericToolControl'
import { PlanApprovalContent } from '../../controls/PlanApprovalContent'

/**
 * ZCode multiplexes three prompts over two RPCs, and the worker records which one
 * arrived as the request's TOOL NAME -- so the tool name is what the dispatchers
 * switch on, exactly as Claude's do.
 *
 *   ExitPlanMode     -> the plan approval (interaction/requestUserInput)
 *   AskUserQuestion  -> a question        (interaction/requestUserInput)
 *   anything else    -> a permission      (interaction/requestPermission)
 */
function zcodeControlKind(payload: Record<string, unknown> | undefined): string {
  return payload ? getToolName(payload) : ''
}

/**
 * The permission content.
 *
 * The shared generic body renders the tool name and its input, which is what a
 * permission prompt needs. ZCode adds a `reason` -- its own explanation of why the
 * call needs approval -- and that is the most useful line in the banner, so it is
 * shown above the input.
 */
const ZCodePermissionContent: Component<ContentProps> = (props) => {
  const reason = createMemo(() =>
    pickString(pickObject(props.request.payload, 'params'), 'reason'))
  return (
    <>
      <Show when={reason()}>
        <div class={styles.bannerReason}>{reason()}</div>
      </Show>
      <GenericToolContent request={props.request} />
    </>
  )
}

/** ZCode control-request content dispatcher. */
export const ZCodeControlContent: Component<ContentProps> = (props) => {
  const kind = createMemo(() => zcodeControlKind(props.request?.payload))
  return (
    <Switch fallback={<ZCodePermissionContent {...props} />}>
      <Match when={kind() === ZCODE_TOOL.ExitPlanMode}>
        <PlanApprovalContent />
      </Match>
    </Switch>
  )
}

/**
 * ZCode control-request actions dispatcher.
 *
 * Every branch sends the SHARED allow/deny envelope unchanged. The worker translates it
 * into the app-server's own reply shape when it forwards it -- a permission decision,
 * or an accept/decline action -- so no ZCode frame is built here, and the plan and
 * question surfaces keep the exact behavior they have for every other provider.
 */
export const ZCodeControlActions: Component<ActionsProps> = (props) => {
  const kind = createMemo(() => zcodeControlKind(props.request?.payload))
  return (
    <Switch fallback={<GenericToolActions {...props} />}>
      <Match when={kind() === ZCODE_TOOL.ExitPlanMode}>
        <ExitPlanModeActions {...props} />
      </Match>
    </Switch>
  )
}

/** Whether a stored ZCode control payload is the AskUserQuestion prompt. */
export function zcodeIsAskUserQuestion(payload: Record<string, unknown>): boolean {
  return getToolName(payload) === ZCODE_TOOL.AskUserQuestion
}
