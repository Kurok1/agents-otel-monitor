/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.5.2
 */

import { getJSON } from './http.ts';

export type PricingClient = 'all' | 'claude' | 'codex';

export interface UnitPriceFields {
  input_per_1m: number | null;
  output_per_1m: number | null;
  cache_read_per_1m: number | null;
  reasoning_output_per_1m: number | null;
}

export interface UsedPricedModel extends UnitPriceFields {
  model: string;
  clients: Array<Exclude<PricingClient, 'all'>>;
  matched: boolean;
  requests: number;
  last_seen: string;
}

export interface PricingModelsResponse {
  enabled: boolean;
  table_entries?: number;
  last_refresh?: string;
  models: UsedPricedModel[];
}

export interface CatalogPricedModel extends UnitPriceFields {
  model: string;
}

export interface PricingCatalogResponse {
  enabled: boolean;
  table_entries?: number;
  last_refresh?: string;
  total_matches: number;
  offset: number;
  limit: number;
  models: CatalogPricedModel[];
}

export const Pricing = {
  used(prefix = '', client: PricingClient = 'all'): Promise<PricingModelsResponse> {
    const query = new URLSearchParams({ client });
    if (prefix) query.set('prefix', prefix);
    return getJSON<PricingModelsResponse>(`/api/pricing/models?${query}`);
  },

  catalog(prefix = '', offset = 0, limit = 100): Promise<PricingCatalogResponse> {
    const query = new URLSearchParams({
      prefix,
      offset: String(offset),
      limit: String(limit),
    });
    return getJSON<PricingCatalogResponse>(`/api/pricing/catalog?${query}`);
  },
};
