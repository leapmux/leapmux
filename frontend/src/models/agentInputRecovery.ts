import type { AgentInputProcessRecovery } from '~/generated/proto/leapmux/v1/agent_pb'

/** One immutable proposal in a user's recovery confirmation. */
export interface InputRecoveryTarget {
  readonly workerId: string
  readonly workerName: string
  readonly agentId: string
  readonly title: string
  readonly recovery: AgentInputProcessRecovery
}

export interface InputRecoveryWorkerFailure {
  readonly workerId: string
  readonly workerName: string
  readonly message: string
}

export interface InputRecoveryDiscovery {
  readonly current: InputRecoveryTarget
  readonly targets: readonly InputRecoveryTarget[]
  readonly failures: readonly InputRecoveryWorkerFailure[]
}

export type InputRecoveryResult = {
  readonly target: InputRecoveryTarget
  readonly status: 'recovered'
} | {
  readonly target: InputRecoveryTarget
  readonly status: 'unconfirmed'
  readonly message: string
}

export interface InputRecoveryActions {
  captureTarget: (agentId: string, recovery: AgentInputProcessRecovery) => InputRecoveryTarget
  discoverTargets: (current: InputRecoveryTarget) => Promise<InputRecoveryDiscovery>
  recoverTargets: (targets: readonly InputRecoveryTarget[]) => Promise<readonly InputRecoveryResult[]>
}

/** Tab IDs belong to a Worker when they address its private RPC channel. */
export function inputRecoveryTargetKey(target: Pick<InputRecoveryTarget, 'workerId' | 'agentId'>): string {
  return JSON.stringify([target.workerId, target.agentId])
}
