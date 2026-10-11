import type { QuestionPrompt } from '../question'

export interface QuestionRequest { questions: QuestionPrompt[] }
export interface QuestionAnswer { header: string, answer: string | null }
export interface QuestionResult {
  answers: QuestionAnswer[]
  /**
   * What the provider states beside the answers, such as a dismissal that answered no
   * question. Absent when it states nothing.
   */
  note?: string
}
