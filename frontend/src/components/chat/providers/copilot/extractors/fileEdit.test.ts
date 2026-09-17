import { describe, expect, it } from 'vitest'
import { COPILOT_TOOL } from '~/generated/contracts/copilot-protocol'
import { copilotPatchText } from './fileEdit'

const PATCH = '*** Begin Patch\n*** Add File: a.ts\n+one\n*** End Patch'

describe('copilotPatchText', () => {
  it('reads the patch off Copilot\'s own argument key', () => {
    expect(copilotPatchText(COPILOT_TOOL.ApplyPatch, { input: PATCH })).toBe(PATCH)
  })

  // An older frame spells the same argument `patch`, and a persisted transcript still
  // holds frames in that shape.
  it('reads the older spelling of the same argument', () => {
    expect(copilotPatchText(COPILOT_TOOL.ApplyPatch, { patch: PATCH })).toBe(PATCH)
  })

  it('keeps the current spelling ahead of the older one', () => {
    expect(copilotPatchText(COPILOT_TOOL.ApplyPatch, { input: PATCH, patch: 'older' })).toBe(PATCH)
  })

  it('states no patch for a call whose arguments carry none', () => {
    expect(copilotPatchText(COPILOT_TOOL.ApplyPatch, {})).toBe('')
  })

  // Only `apply_patch` sends a patch. Another tool's `input` argument is that tool's
  // own text, and reading it as a patch would hand the shared reader a string it must
  // then refuse.
  it('states no patch for a tool that sends none', () => {
    expect(copilotPatchText(COPILOT_TOOL.View, { input: PATCH })).toBe('')
  })

  it('states no patch for an empty tool name', () => {
    expect(copilotPatchText('', { input: PATCH })).toBe('')
  })
})
