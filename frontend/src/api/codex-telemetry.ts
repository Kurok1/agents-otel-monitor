/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 3.2.0
 */

export interface CostMetadata {
  cost_available?: boolean;
  cost_source?: 'native' | 'local' | 'legacy' | 'mixed' | 'unknown' | 'authoritative';
  cost_coverage?: string;
  native_turn_count?: number;
  legacy_cost_included?: boolean;
  local_estimate_usd?: number | null;
}

export function costSourceLabel(value: CostMetadata): string {
  const sources = {
    native: '原生估值', local: '本地估价', legacy: '历史估价',
    mixed: '按会话选源', unknown: '金额未知', authoritative: '自报成本',
  };
  let label = sources[value.cost_source ?? 'unknown'];
  if ((value.native_turn_count ?? 0) > 0) label += ` · ${value.native_turn_count} 个原生 Turn · 覆盖未知`;
  if (value.legacy_cost_included) label += ' · 含历史口径';
  return label;
}

export interface TTFTSummary {
  samples: number;
  mean_ms: number | null;
  p50_ms: number | null;
  p95_ms: number | null;
}

export interface CodexTelemetry {
  range: string;
  ttft: TTFTSummary;
  models: Array<{
    model: string;
    ttft: TTFTSummary;
    usage: Array<{
      token_type: string;
      response_tokens: number | null;
      turn_tokens: number | null;
      response_samples: number;
      turn_samples: number;
      difference: number | null;
    }>;
  }>;
}
