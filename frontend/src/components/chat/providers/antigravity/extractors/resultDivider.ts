import type { TurnEnd } from '../../../model/divider'
import { ANTIGRAVITY_FRAME } from '~/generated/contracts/antigravity-protocol'
import { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '~/lib/jsonPick'
import { turnEndLabel } from '../../../turnEndLabel'

export function antigravityResultDivider(parsed: unknown, completion?: MessageCompletion): TurnEnd | null {
  if (!isObject(parsed) || parsed.name !== ANTIGRAVITY_FRAME.TurnEnd)
    return null
  if (completion === MessageCompletion.INTERRUPTED) {
    return {
      label: turnEndLabel('interrupted'),
    }
  }
  if (completion === MessageCompletion.ERROR) {
    return {
      label: turnEndLabel('failed'),
      isError: true,
    }
  }
  return {
    label: turnEndLabel('ended'),
  }
}
