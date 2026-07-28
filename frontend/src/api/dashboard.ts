// Dashboard data layer.
//
// Talks to /api/usage/{snapshot,trends,rankings} on the same origin
// (Vite dev proxy points to the Go server in dev mode). Adapts the wire
// shape (snake_case, group-keyed) to a UI-friendly shape (camelCase,
// id-keyed with label/tier/color baked in).
//
// Groups are dynamic: the backend Classifier decides them from raw model
// names + optional user-configured rules. The frontend treats group ids
// as arbitrary strings, picks colors via hash → palette, and derives a
// display label/tier from the group key (Claude family pattern → branded
// label; OpenAI/Codex families → GPT/o-series branded label, tier "OpenAI";
// everything else → group name verbatim, tier "第三方模型").

import { getJSON } from './http';

export type Range = 'day' | 'week' | 'month';
export type Since = '7d' | '30d' | 'all';
export type Client = 'all' | 'claude' | 'codex';

export interface ModelMeta {
  id: string;
  label: string;
  tier: string;
  color: string;
}

export interface ModelBreakdown extends ModelMeta {
  requests: number;
  tokens_in: number;
  tokens_out: number;
  cache_tokens: number;
  cost: number;
  share: number;
}

// SeriesPoint maps group id → token count for one bucket of the trends chart.
// Groups missing from a bucket are treated as zero.
export interface SeriesPoint {
  date: string;
  label: string;
  values: Record<string, number>;
}

export interface ToolUsage {
  name: string;
  count: number;
}

export interface SkillUsage {
  name: string;
  activations: number;
}

// One calendar day of the 360-day usage heatmap. Raw components feed the
// tooltip; `score` (composite intensity, [0,1]) drives the cell color.
export interface HeatmapDay {
  date: string; // YYYY-MM-DD
  tokens: number;
  cost: number;
  requests: number;
  score: number;
}

// 价目表一行:单价为 $/1M tokens;null = 计价表缺该字段;matched=false = 未收录。
export interface PricedModel {
  model: string;
  clients: string[];
  matched: boolean;
  input_per_1m: number | null;
  output_per_1m: number | null;
  cache_read_per_1m: number | null;
  reasoning_output_per_1m: number | null;
  requests: number;
  last_seen: string;
}

export interface DashboardData {
  updatedAt: string;
  range: Range;
  tokens: {
    in: number;
    out: number;
    total: number;
    prev_total: number;
    sparkline: number[];
  };
  cost: {
    total: number;
    prev_total: number;
    sparkline: number[];
    cost_estimated: boolean;
  };
  cache: {
    hit_rate: number | null;
    read_tokens: number;
    creation_tokens: number;
  };
  requests: {
    total: number;
    prev_total: number;
    sparkline: number[];
  };
  models: ModelBreakdown[];
  series: {
    groups: ModelMeta[]; // legend order from API + UI metadata
    points: SeriesPoint[];
  };
  tools: ToolUsage[];
  skills: SkillUsage[];
  heatmap: {
    weights: { tokens: number; cost: number; requests: number };
    points: HeatmapDay[];
  };
  rates: {
    bucketInterval: string;
    speed: {
      groups: ModelMeta[];
      points: SeriesPoint[]; // date=桶起点 RFC3339;缺 key = 断线
      current: number | null;
      previous: number | null;
    };
    throughput: {
      types: string[];
      points: SeriesPoint[]; // 空桶为 0
    };
  };
  pricing: {
    enabled: boolean;
    tableEntries: number;
    models: PricedModel[];
  };
}

// ─────────────────────────────────────────────────────────────────────
// Group → display metadata
// ─────────────────────────────────────────────────────────────────────

const PALETTE = [
  '#D97757', // Claude clay (opus signature)
  '#3B6FD4', // sonnet blue
  '#2D7D46', // haiku green
  '#8B5A2B', // amber
  '#7B4E9A', // violet
  '#D4860A', // gold
  '#1E7A99', // teal
  '#A8502C', // rust
  '#274EA0', // indigo
  '#1E5730', // forest
];

const CLAUDE_TIER: Record<string, string> = {
  fable: '顶级 · 最强推理',
  opus: '旗舰 · 复杂推理',
  sonnet: '主力 · 编码 / Agent',
  haiku: '轻量 · 快速 / 大批量',
};

// Minor version is optional: Fable ids are single-segment (`fable-5`),
// opus/sonnet/haiku stay `family-MAJOR.MINOR`.
const CLAUDE_GROUP_RE = /^(opus|sonnet|haiku|fable)-(\d+(?:\.\d+)?)$/;

// OpenAI / Codex families (gpt-*, chatgpt-*, o1/o3/o4 reasoning) arrive via
// Codex telemetry. Brand them like a first-party family instead of the generic
// 第三方模型 tier. Checked after the Claude pattern, so `opus-*` never matches.
const OPENAI_GROUP_RE = /^(gpt[-.\d]|chatgpt|o[1-9])/i;

function openaiLabel(group: string): string {
  return group
    .replace(/^chatgpt/i, 'ChatGPT')
    .replace(/^gpt/i, 'GPT')
    .replace(/codex/i, 'Codex');
}

// Stable hash for deterministic color assignment. Same group string always
// produces the same palette slot across reloads.
function hashString(s: string): number {
  let h = 2166136261;
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i);
    h = Math.imul(h, 16777619);
  }
  return h >>> 0;
}

function colorFor(group: string): string {
  return PALETTE[hashString(group) % PALETTE.length];
}

function capitalize(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1);
}

export function metaForGroup(group: string): ModelMeta {
  const sourceMatch = group.match(/ · (Claude|Codex)$/);
  if (sourceMatch) {
    const base = group.slice(0, -sourceMatch[0].length);
    const meta = metaForGroup(base);
    return {
      ...meta,
      id: group,
      label: `${meta.label} · ${sourceMatch[1]}`,
      color: colorFor(group),
    };
  }
  const m = group.match(CLAUDE_GROUP_RE);
  if (m) {
    const family = m[1];
    const ver = m[2];
    return {
      id: group,
      label: `Claude ${capitalize(family)} ${ver}`,
      tier: CLAUDE_TIER[family] ?? '',
      color: colorFor(group),
    };
  }
  if (OPENAI_GROUP_RE.test(group)) {
    return {
      id: group,
      label: openaiLabel(group),
      tier: 'OpenAI',
      color: colorFor(group),
    };
  }
  return {
    id: group,
    label: group,
    tier: '第三方模型',
    color: colorFor(group),
  };
}

// ─────────────────────────────────────────────────────────────────────
// Wire types (snake_case, mirrors internal/dashboard/types.go)
// ─────────────────────────────────────────────────────────────────────

interface SnapshotWire {
  updated_at: string;
  range: Range;
  tokens: {
    in: number;
    out: number;
    total: number;
    prev_total: number;
    sparkline: number[];
  };
  cost: {
    total: number;
    prev_total: number;
    sparkline: number[];
    cost_estimated: boolean;
  };
  cache: {
    hit_rate: number | null;
    read_tokens: number;
    creation_tokens: number;
  };
  requests: {
    total: number;
    prev_total: number;
    sparkline: number[];
  };
  models: Array<{
    group: string;
    requests: number;
    tokens_in: number;
    tokens_out: number;
    cache_tokens: number;
    cost: number;
    share: number;
  }>;
}

interface TrendsWire {
  range: Range;
  groups: string[];
  points: Array<{
    date: string;
    label: string;
    values: Record<string, number>;
  }>;
}

interface RankingsWire {
  since: Since;
  tools: ToolUsage[];
  skills: SkillUsage[];
}

interface HeatmapWire {
  updated_at: string;
  days: number;
  timezone: string;
  weights: { tokens: number; cost: number; requests: number };
  points: Array<{
    date: string;
    tokens: number;
    cost: number;
    requests: number;
    score: number;
  }>;
}

interface RatesWire {
  range: Range;
  bucket_interval: string;
  speed: {
    groups: string[];
    points: Array<{ ts: string; label: string; values: Record<string, number> }>;
    current: number | null;
    previous: number | null;
  };
  throughput: {
    types: string[];
    points: Array<{ ts: string; label: string; values: Record<string, number> }>;
  };
}

interface PricingWire {
  enabled: boolean;
  table_entries?: number;
  last_refresh?: string;
  models: PricedModel[];
}

// ─────────────────────────────────────────────────────────────────────
// Public API
// ─────────────────────────────────────────────────────────────────────

export const Dashboard = {
  async fetch(range: Range = 'day', since: Since = '7d', client: Client = 'all'): Promise<DashboardData> {
    const [snap, trends, rankings, heatmap, rates, pricing] = await Promise.all([
      getJSON<SnapshotWire>(`/api/usage/snapshot?range=${range}&client=${client}`),
      getJSON<TrendsWire>(`/api/usage/trends?range=${range}&client=${client}`),
      getJSON<RankingsWire>(`/api/usage/rankings?since=${since}&client=${client}`),
      getJSON<HeatmapWire>(`/api/usage/heatmap?client=${client}`),
      getJSON<RatesWire>(`/api/usage/rates?range=${range}&client=${client}`),
      getJSON<PricingWire>(`/api/pricing/models?client=${client}`),
    ]);
    return adapt(snap, trends, rankings, heatmap, rates, pricing);
  },
};

function adapt(
  snap: SnapshotWire,
  trends: TrendsWire,
  rankings: RankingsWire,
  heatmap: HeatmapWire,
  rates: RatesWire,
  pricing: PricingWire,
): DashboardData {
  // rate 点复用 SeriesPoint 形状:wire 的 ts 落到 date 字段
  const ratePoints = (
    pts: Array<{ ts: string; label: string; values: Record<string, number> }>,
  ): SeriesPoint[] => pts.map(p => ({ date: p.ts, label: p.label, values: p.values ?? {} }));

  return {
    updatedAt: snap.updated_at,
    range: snap.range,
    tokens: snap.tokens,
    cost: snap.cost,
    cache: snap.cache,
    requests: snap.requests,
    models: snap.models.map(m => ({
      ...metaForGroup(m.group),
      requests: m.requests,
      tokens_in: m.tokens_in,
      tokens_out: m.tokens_out,
      cache_tokens: m.cache_tokens,
      cost: m.cost,
      share: m.share,
    })),
    series: {
      groups: trends.groups.map(g => metaForGroup(g)),
      points: trends.points.map(p => ({
        date: p.date,
        label: p.label,
        values: p.values ?? {},
      })),
    },
    tools: rankings.tools,
    skills: rankings.skills,
    heatmap: { weights: heatmap.weights, points: heatmap.points },
    rates: {
      bucketInterval: rates.bucket_interval,
      speed: {
        groups: rates.speed.groups.map(g => metaForGroup(g)),
        points: ratePoints(rates.speed.points),
        current: rates.speed.current,
        previous: rates.speed.previous,
      },
      throughput: {
        types: rates.throughput.types,
        points: ratePoints(rates.throughput.points),
      },
    },
    pricing: {
      enabled: pricing.enabled,
      tableEntries: pricing.table_entries ?? 0,
      models: pricing.models ?? [],
    },
  };
}
