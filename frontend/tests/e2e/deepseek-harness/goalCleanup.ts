export interface DeepseekHarnessGoalOwner {
  readonly agentId: string
  readonly workerId: string
}

export interface DeepseekHarnessGoalCleanupActions {
  clearGoal: (owner: DeepseekHarnessGoalOwner) => Promise<void>
  closeAgent: (owner: DeepseekHarnessGoalOwner) => Promise<void>
  releaseReplies: () => Promise<void>
}

export async function cleanupDeepseekHarnessGoal(owner: DeepseekHarnessGoalOwner, actions: DeepseekHarnessGoalCleanupActions): Promise<void> {
  const failures: unknown[] = []
  const validOwner = owner.agentId.trim() !== '' && owner.workerId.trim() !== ''
  try {
    if (!validOwner)
      throw new Error('The native goal cleanup requires its captured root and Worker identities.')
    await actions.clearGoal(owner)
  }
  catch (cause) {
    failures.push(cause)
    if (validOwner) {
      try {
        await actions.closeAgent(owner)
      }
      catch (closeError) {
        failures.push(closeError)
      }
    }
  }
  try {
    await actions.releaseReplies()
  }
  catch (releaseError) {
    failures.push(releaseError)
  }
  if (failures.length === 1)
    throw failures[0]
  if (failures.length > 1)
    throw new AggregateError(failures, 'The native goal cleanup failed.')
}
