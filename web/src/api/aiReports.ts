import { http } from './request'
import type { PagedData } from '../types'

export interface AIReport {
  id: string
  period: string
  start_time: string
  end_time: string
  status: string
  report: string
  error?: string
  created_at: string
  updated_at: string
}

export const getAIReports = (offset = 0, limit = 20) =>
  http.get<PagedData<AIReport>>('/ai-reports', { params: { offset, limit } })

export const createAIReport = (data: {
  period: string
  start_time: string
  end_time: string
}) => http.post<AIReport>('/ai-reports', data)

export const getAIReportById = (id: string) =>
  http.get<AIReport>(`/ai-reports/${id}`)
