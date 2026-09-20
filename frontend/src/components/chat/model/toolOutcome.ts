export type RetainedToolOutcome = 'succeeded' | 'failed' | 'interrupted'

export type ProviderToolOutcome = RetainedToolOutcome | 'declined'

export type ToolOutcome = ProviderToolOutcome | 'incomplete'
