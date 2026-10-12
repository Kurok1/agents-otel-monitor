/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 3.2.0
 */

import assert from 'node:assert/strict';
import { afterEach, test } from 'node:test';
import { Dashboard } from '../src/api/dashboard.ts';

const originalFetch = globalThis.fetch;
afterEach(() => { globalThis.fetch = originalFetch; });

function stubDashboard(seen: string[], telemetryStatus: number) {
  const responses: Record<string, unknown> = {
    snapshot: {
      range: 'day', models: [], tokens: {}, cache: {}, requests: {},
      cost: { total: 0, cost_available: true, cost_source: 'native', native_turn_count: 1, cost_estimated: true, local_estimate_usd: 0.5 },
    },
    trends: { groups: [], points: [] }, rankings: { tools: [], skills: [] }, heatmap: { points: [] },
    rates: { speed: { groups: [], points: [] }, throughput: { types: [], points: [] } },
    realtime: { current: null, previous: null },
    telemetry: { range: 'day', ttft: { samples: 0, mean_ms: null, p50_ms: null, p95_ms: null }, models: [] },
  };
  globalThis.fetch = async input => {
    const url = String(input);
    seen.push(url);
    const key = url.split('?')[0].split('/').at(-1)!;
    return new Response(JSON.stringify(responses[key]), {
      status: key === 'telemetry' ? telemetryStatus : 200,
      headers: { 'Content-Type': 'application/json' },
    });
  };
}

test('native zero stays available and empty TTFT stays unknown', async () => {
  const seen: string[] = [];
  stubDashboard(seen, 200);
  const value = await Dashboard.fetch('day', '7d', 'codex');
  assert.equal(value.cost.total, 0);
  assert.equal(value.cost.cost_available, true);
  assert.equal(value.cost.local_estimate_usd, 0.5);
  assert.equal(value.codexTelemetry?.ttft.mean_ms, null);
  assert.ok(seen.includes('/api/usage/codex/telemetry?range=day'));
});

test('optional telemetry failure preserves primary dashboard data', async () => {
  const seen: string[] = [];
  stubDashboard(seen, 500);
  const value = await Dashboard.fetch('day', '7d', 'codex');
  assert.equal(value.codexTelemetry, null);
  assert.equal(value.cost.cost_source, 'native');
});

test('Claude dashboard does not request Codex telemetry', async () => {
  const seen: string[] = [];
  stubDashboard(seen, 500);
  await Dashboard.fetch('day', '7d', 'claude');
  assert.ok(seen.every(url => !url.includes('/codex/telemetry')));
});
