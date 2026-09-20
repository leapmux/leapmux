export type ControlResponseSummary
  = | { kind: 'label', text: string }
    | { kind: 'feedback', message: string }
