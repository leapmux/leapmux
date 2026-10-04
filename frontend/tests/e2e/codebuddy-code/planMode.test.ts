import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { AvailableOptionGroupSchema } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codebuddyPlanOptionSnapshot } from './planMode'

function group(id: string, currentValue: string) {
  return create(AvailableOptionGroupSchema, { id, currentValue })
}

describe('codebuddyPlanOptionSnapshot', () => {
  it('preserves an existing empty native effort selection', () => {
    expect(codebuddyPlanOptionSnapshot([group('model', 'native-model'), group('effort', '')])).toEqual({ model: 'native-model', effort: '' })
  })
  it('preserves the exact explicit model and effort values', () => {
    expect(codebuddyPlanOptionSnapshot([group('model', 'native-model'), group('effort', 'low')])).toEqual({ model: 'native-model', effort: 'low' })
  })
  it.each([
    { groups: [] },
    { groups: [group('model', 'native-model')] },
    { groups: [group('effort', 'low')] },
    { groups: [group('model', 'a'), group('model', 'b'), group('effort', 'low')] },
    { groups: [group('model', 'native-model'), group('effort', 'low'), group('effort', 'high')] },
  ])('rejects a missing or repeated native group', ({ groups }) => {
    expect(() => codebuddyPlanOptionSnapshot(groups)).toThrow('catalog is absent')
  })
})
