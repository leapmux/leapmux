export interface QuestionOption {
  value?: string
  label: string
  description?: string
  preview?: string
}

export interface QuestionPrompt {
  header?: string
  question: string
  options: QuestionOption[]
}

export interface ControlQuestion extends QuestionPrompt {
  id?: string
  multiSelect?: boolean
  allowEmpty?: boolean
}
