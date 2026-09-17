import type { QuestionIR } from '../questionBody'

export interface QuestionRequest { questions: QuestionIR[] }
export interface QuestionAnswer { header: string, answer: string | null }
export interface QuestionResult { answers: QuestionAnswer[] }
