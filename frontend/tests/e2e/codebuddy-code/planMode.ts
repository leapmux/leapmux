import type { AvailableOptionGroup } from '../../../src/generated/proto/leapmux/v1/agent_pb'

/** Record both native selections before or after a Plan mode change. */
export function codebuddyPlanOptionSnapshot(groups: readonly AvailableOptionGroup[]): { model: string, effort: string } {
  const model = groups.filter(group => group.id === 'model')
  const effort = groups.filter(group => group.id === 'effort')
  const modelGroup = model[0]
  const effortGroup = effort[0]
  if (model.length !== 1 || effort.length !== 1 || !modelGroup || !effortGroup)
    throw new Error('The native CodeBuddy model or effort catalog is absent.')
  return { model: modelGroup.currentValue, effort: effortGroup.currentValue }
}
