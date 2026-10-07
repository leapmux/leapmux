import { describe, expect, it } from 'vitest'
import { gitRepositoryWorkingDir } from '../helpers/providerWorkingDir'
import { COPILOT_AGENT } from './scenarios'

describe('COPILOT_AGENT', () => {
  // GitHub Copilot CLI reads its custom instructions from each directory between its working directory and the root of
  // the git repository, and from directories above that when no repository holds the working directory. Its one switch,
  // `--no-custom-instructions`, also turns off the AGENTS.md of the working directory that `workspace-trust.spec.ts`
  // proves. The run root above every working directory holds the sentinel files (`../helpers/ancestorInstructions.ts`).
  it('opens in the root of a git repository of its own, where Copilot stops its search for instructions', () => {
    expect(COPILOT_AGENT.workingDir).toBe(gitRepositoryWorkingDir)
  })
})
