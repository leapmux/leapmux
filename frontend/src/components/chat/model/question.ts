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
  /**
   * How many options a multiple-selection answer must choose, when the native
   * question states counts. Absent means the provider states none: any number
   * of options (including none, beside a typed answer) is a complete answer.
   * The control enforces both counts; a restored selection outside them stays
   * on screen until the user corrects it.
   */
  minimumSelections?: number
  /** Upper counterpart of {@link minimumSelections}; the two are independent. */
  maximumSelections?: number
}
