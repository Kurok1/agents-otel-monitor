/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.5.2
 */

import assert from 'node:assert/strict';
import { afterEach, test } from 'node:test';

import { Pricing } from '../src/api/pricing.ts';

const originalFetch = globalThis.fetch;

afterEach(() => {
  globalThis.fetch = originalFetch;
});

function stubJSON(body: unknown, seen: string[]) {
  globalThis.fetch = async input => {
    seen.push(String(input));
    return new Response(JSON.stringify(body), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    });
  };
}

test('used model prices request the seen-model endpoint with a prefix', async () => {
  const seen: string[] = [];
  stubJSON({ enabled: true, models: [] }, seen);

  await Pricing.used('GPT-5.6');

  assert.deepEqual(seen, ['/api/pricing/models?client=all&prefix=GPT-5.6']);
});

test('full model prices request the catalog endpoint with prefix and pagination', async () => {
  const seen: string[] = [];
  stubJSON({ enabled: true, models: [], total_matches: 0 }, seen);

  await Pricing.catalog('claude-opus', 100, 100);

  assert.deepEqual(seen, [
    '/api/pricing/catalog?prefix=claude-opus&offset=100&limit=100',
  ]);
});
