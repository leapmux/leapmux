import type { QuestionPrompt } from '../question'

export interface QuestionRequest { questions: QuestionPrompt[] }
export interface QuestionAnswer { header: string, answer: string | null }
export interface QuestionResult { answers: QuestionAnswer[] }
