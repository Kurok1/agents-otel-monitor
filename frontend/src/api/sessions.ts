/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.0.0
 */

// Session list + detail data layer. Talks to /api/sessions and
// /api/sessions/{id} on the same origin (Vite dev proxy → Go server).

import { getJSON } from './http';
import type { CostMetadata } from './codex-telemetry';

export type SessionClient = 'claude' | 'codex';

export interface SessionSummary extends CostMetadata {
  session_id: string;
  client: SessionClient;
  first_active: string;
  last_active: string;
  tokens: number;
  requests: number;
  tool_calls: number;
  skill_activations: number;
  cost?: number;
  cost_estimated: boolean;
}

export interface SessionListResponse {
  updated_at: string;
  sessions: SessionSummary[];
}

// Pie slices. Tools key the count as `count`, skills as `activations` —
// mirrors internal/dashboard ToolRank / SkillRank JSON tags.
export interface ToolSlice {
  name: string;
  count: number;
}
export interface SkillSlice {
  explicit: number;
  implicit: number;
  name: string;
  activations: number;
}

export interface SessionDetail extends CostMetadata {
  session_id: string;
  client: SessionClient;
  first_active: string;
  last_active: string;
  tokens: number;
  requests: number;
  tool_calls: number;
  skill_activations: number;
  cost?: number;
  cost_estimated: boolean;
  tools: ToolSlice[];
  skills: SkillSlice[];
  // codex-only:四个原始 token 维度(子集口径:cached ⊂ input、reasoning ⊂ output)
  token_detail?: {
    input: number;
    output: number;
    cached: number;
    cache_write: number;
    reasoning: number;
  };
}

export const Sessions = {
  list(limit = 30, client: 'all' | SessionClient = 'all'): Promise<SessionListResponse> {
    return getJSON<SessionListResponse>(`/api/sessions?limit=${limit}&client=${client}`);
  },
  detail(id: string, client?: SessionClient): Promise<SessionDetail> {
    const suffix = client ? `?client=${client}` : '';
    return getJSON<SessionDetail>(`/api/sessions/${encodeURIComponent(id)}${suffix}`);
  },
};
