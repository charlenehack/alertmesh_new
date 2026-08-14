import { http } from './request'

export interface SavedQuery {
  id: string
  name: string
  data_source_kind: 'prometheus' | 'opensearch' | 'elastic'
  data_source_id?: string
  natural_language: string
  query_text: string
  is_shared: boolean
  created_by?: string
  created_at: string
  updated_at: string
}

export interface GenerateQueryRequest {
  data_source_kind: SavedQuery['data_source_kind']
  natural_language: string
}

export interface GenerateQueryResponse {
  query_text: string
}

export interface ExecuteQueryRequest {
  data_source_kind: SavedQuery['data_source_kind']
  data_source_id?: string
  query_text: string
  start_time?: string
  end_time?: string
  step?: string
}

export interface SavedQueryWritePayload {
  name: string
  data_source_kind: SavedQuery['data_source_kind']
  data_source_id?: string
  natural_language: string
  query_text: string
  is_shared?: boolean
}

export interface SummarizeResultRequest {
  data_source_kind: SavedQuery['data_source_kind']
  natural_language: string
  result: unknown
}

export interface SummarizeResultResponse {
  summary: string
}

export const generateObservabilityQuery = (data: GenerateQueryRequest) =>
  http.post<GenerateQueryResponse>('/observability/generate', data)

export const summarizeObservabilityResult = (data: SummarizeResultRequest) =>
  http.post<SummarizeResultResponse>('/observability/summarize', data)

export const executeObservabilityQuery = (data: ExecuteQueryRequest) =>
  http.post<Record<string, unknown>>('/observability/execute', data)

export const getSavedQueries = (kind?: SavedQuery['data_source_kind']) =>
  http.get<SavedQuery[]>('/observability/queries', kind ? { params: { kind } } : undefined)

export const createSavedQuery = (data: SavedQueryWritePayload) =>
  http.post<SavedQuery>('/observability/queries', data)

export const deleteSavedQuery = (id: string) =>
  http.delete(`/observability/queries/${id}`)
