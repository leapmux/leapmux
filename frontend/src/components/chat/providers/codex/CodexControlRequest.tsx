import type { Component } from 'solid-js'
import type { ControlAllowChoicePill } from '../../controls/ControlPillGroups'
import type { ActionsProps, ContentProps } from '../../controls/types'
import type { CodexDecision } from './controlResponse'

import { createMemo, Match, Show, Switch } from 'solid-js'
import { buildAllowResponse, buildDenyResponse, getToolInput, getToolName } from '~/utils/controlResponse'
import * as styles from '../../ControlRequestBanner.css'
import { CollapsibleText } from '../../controls/CollapsibleText'
import { ControlDecisionFooter } from '../../controls/ControlDecisionFooter'
import { buildSessionPermissionPill, createSessionPermissionPresetChoice, respondThenApplyPermissionPreset } from '../../controls/permissionPresets'
import { createPlanApprovalState, planApprovalSwitches } from '../../controls/planApproval'
import { CONTROL_ALLOW_CHOICE_ID, createControlChoice } from '../../controls/types'
import {
  ALLOW_AS_LABEL,
  CODEX_PERMISSION_SCOPE_OPTIONS,
  codexDecisionKey,
  codexDecisionLabel,
  codexRequestedPermissions,
  getCodexParams,
  resolveCodexDecisions,
  sendCodexDecision,
  sendCodexPermissionsResponse,
  sendCodexPlanPromptResponse,
} from './controlResponse'

/** Codex-specific control request content. */
export const CodexControlContent: Component<ContentProps> = (props) => {
  const toolName = () => getToolName(props.request.payload)
  const params = () => getCodexParams(props.request.payload)
  const method = () => props.request.payload.method as string | undefined
  const reason = () => params()?.reason as string | undefined
  const command = () => params()?.command as string | undefined
  const cwd = () => params()?.cwd as string | undefined
  const permissions = () => codexRequestedPermissions(props.request.payload)
  const title = () => {
    const m = method()
    if (m === 'item/commandExecution/requestApproval')
      return 'Command Execution'
    if (m === 'item/fileChange/requestApproval')
      return 'File Change'
    if (m === 'item/permissions/requestApproval')
      return 'Permission Request'
    return 'Approval Required'
  }

  return (
    <Switch
      fallback={(
        <>
          <div class={styles.controlBannerTitle}>{title()}</div>
          <Show when={reason()}>
            <div class={styles.bannerReason}>{reason()}</div>
          </Show>
          <Show when={command()}>
            <CollapsibleText text={command()!} maxLines={6} class={styles.bannerCodeBlock} />
          </Show>
          <Show when={cwd()}>
            <div class={styles.bannerHint}>
              {'cwd: '}
              {cwd()}
            </div>
          </Show>
          <Show when={method() === 'item/permissions/requestApproval'}>
            <CollapsibleText text={JSON.stringify(permissions(), null, 2)} maxLines={6} class={styles.bannerCodeBlock} />
          </Show>
        </>
      )}
    >
      <Match when={toolName() === 'CodexPlanModePrompt'}>
        <div class={styles.controlBannerTitle}>Implement the proposed plan?</div>
      </Match>
    </Switch>
  )
}

const CodexPermissionsActions: Component<ActionsProps> = (props) => {
  const allowChoice = createControlChoice(() => props.answerState, CONTROL_ALLOW_CHOICE_ID)
  const permissionChoice = createSessionPermissionPresetChoice(props)
  // A saved key the offered pair no longer holds clamps to Once, which is the
  // narrowest grant. A wider one must be chosen, never inherited.
  const selectedScope = (): 'turn' | 'session' =>
    (allowChoice.choice() === 'session' ? 'session' : 'turn')
  // A memo, like its sibling in `CodexControlActions`: the footer reads the
  // accessor twice per pass (once for `leadingOptions`, once for the `Show`).
  const allowChoicePill = createMemo<ControlAllowChoicePill>(() => ({
    label: ALLOW_AS_LABEL,
    options: CODEX_PERMISSION_SCOPE_OPTIONS,
    selected: selectedScope(),
    onSelect: allowChoice.setChoice,
  }))
  const handleAllow = () => respondThenApplyPermissionPreset(
    sendCodexPermissionsResponse(
      props.onRespond,
      props.request.requestId,
      codexRequestedPermissions(props.request.payload),
      selectedScope(),
    ),
    props.presets,
    permissionChoice.choice(),
  )
  return (
    <ControlDecisionFooter
      hasEditorContent={props.hasEditorContent}
      onSendFeedback={props.onTriggerSend}
      negativeAction={{
        label: 'Deny',
        testId: 'control-deny-btn',
        onSelect: () => sendCodexPermissionsResponse(props.onRespond, props.request.requestId, {}, 'turn'),
      }}
      positiveAction={{ label: 'Allow', testId: 'control-allow-btn', onSelect: handleAllow }}
      allowChoicePill={allowChoicePill}
      permissionPill={() => buildSessionPermissionPill(props.presets, permissionChoice)}
    />
  )
}

/** Codex plan-mode prompt actions with clear-context and permission pills. */
const CodexPlanModePromptActions: Component<ActionsProps> = (props) => {
  const planApproval = createPlanApprovalState(props)

  const handleApprove = () => sendCodexPlanPromptResponse(
    props.onRespond,
    buildAllowResponse(props.request.requestId, getToolInput(props.request.payload), {
      permissionMode: planApproval.permissionMode(),
      clearContext: planApproval.clearContext(),
    }),
  )

  return (
    <ControlDecisionFooter
      hasEditorContent={props.hasEditorContent}
      onSendFeedback={props.onTriggerSend}
      negativeAction={{
        label: 'Reject',
        testId: 'control-deny-btn',
        onSelect: () => sendCodexPlanPromptResponse(props.onRespond, buildDenyResponse(props.request.requestId, '')),
      }}
      positiveAction={{ label: 'Approve', testId: 'control-allow-btn', onSelect: handleApprove }}
      switches={() => planApprovalSwitches(planApproval)}
      permissionPill={planApproval.permissionPill}
    />
  )
}

/**
 * One decision as a footer action, or nothing when Codex offered that polarity
 * no decision at all. The footer then draws no button, rather than one that
 * sends a token the request never carried.
 */
function codexAction(
  decision: CodexDecision | undefined,
  testId: string,
  onSelect: (decision: CodexDecision) => void,
) {
  if (!decision)
    return undefined
  return { label: codexDecisionLabel(decision), testId, onSelect: () => onSelect(decision) }
}

/** Codex-specific control request action buttons. */
export const CodexControlActions: Component<ActionsProps> = (props) => {
  const toolName = () => getToolName(props.request.payload)
  const method = () => props.request.payload.method as string | undefined
  const params = () => getCodexParams(props.request.payload)
  const decisions = createMemo(() => resolveCodexDecisions(params()?.availableDecisions))
  const allowChoice = createControlChoice(() => props.answerState, CONTROL_ALLOW_CHOICE_ID)
  const permissionChoice = createSessionPermissionPresetChoice(props)

  // A saved key the live payload no longer offers clamps to the first pill,
  // which `codexAllowChoices` guarantees is Codex's one-turn `accept`. Never to
  // a remembering decision: a grant that outlives the turn must be chosen.
  const selectedAllowChoice = createMemo(() => {
    const allow = decisions().allowChoices
    if (!allow)
      return undefined
    return allow.choices.find(choice => choice.key === allowChoice.choice()) ?? allow.choices[0]
  })

  const allowChoicePill = createMemo<ControlAllowChoicePill | undefined>(() => {
    const allow = decisions().allowChoices
    if (!allow)
      return undefined
    return {
      label: ALLOW_AS_LABEL,
      options: allow.options,
      selected: selectedAllowChoice()?.key ?? allow.options[0].key,
      onSelect: allowChoice.setChoice,
    }
  })

  const handleDecision = (decision: CodexDecision) => sendCodexDecision(
    props.onRespond,
    props.request.requestId,
    decision,
  )

  // The pill's decision when a group renders, else the one allow decision the
  // request carried -- which `codexAction` already proved present, because it
  // draws no button without it.
  const handleAllow = (positive: CodexDecision) => respondThenApplyPermissionPreset(
    handleDecision(selectedAllowChoice()?.decision ?? positive),
    props.presets,
    permissionChoice.choice(),
  )

  return (
    <Switch
      fallback={(
        <ControlDecisionFooter
          hasEditorContent={props.hasEditorContent}
          onSendFeedback={props.onTriggerSend}
          negativeAction={codexAction(decisions().negative, 'control-deny-btn', decision => handleDecision(decision))}
          positiveAction={codexAction(decisions().positive, 'control-allow-btn', handleAllow)}
          allowChoicePill={allowChoicePill}
          permissionPill={() => buildSessionPermissionPill(props.presets, permissionChoice)}
          additionalActions={() => decisions().additional.map(decision => ({
            label: codexDecisionLabel(decision),
            testId: `control-decision-${codexDecisionKey(decision)}`,
            onSelect: () => handleDecision(decision),
            outline: decision === 'decline' || decision === 'cancel',
          }))}
        />
      )}
    >
      <Match when={toolName() === 'CodexPlanModePrompt'}>
        <CodexPlanModePromptActions {...props} />
      </Match>
      <Match when={method() === 'item/permissions/requestApproval'}>
        <CodexPermissionsActions {...props} />
      </Match>
    </Switch>
  )
}
