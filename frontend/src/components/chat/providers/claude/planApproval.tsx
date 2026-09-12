import type { Component } from 'solid-js'
import type { ControlRequest } from '~/stores/control.store'
import { createMemo } from 'solid-js'
import { isObject, pickString } from '~/lib/jsonPick'
import { getToolInput } from '~/utils/controlResponse'
import { PlanApprovalContent } from '../../controls/PlanApprovalContent'

/** Extract Claude's requested permissions for the shared approval content. */
export const ClaudePlanApprovalContent: Component<{ request: ControlRequest }> = (props) => {
  const source = createMemo(() => {
    const input = getToolInput(props.request.payload)
    const permissions = Array.isArray(input.allowedPrompts)
      ? input.allowedPrompts.flatMap((value) => {
          if (!isObject(value))
            return []
          const tool = pickString(value, 'tool')
          const prompt = pickString(value, 'prompt')
          return tool.trim() && prompt.trim() ? [{ tool, prompt }] : []
        })
      : []
    return permissions
  })
  return <PlanApprovalContent permissions={source()} />
}
