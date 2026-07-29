/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.5.2
 */

import { useEffect, useRef, useState } from 'react';
import { Pricing } from '../api/pricing';
import type {
  CatalogPricedModel,
  PricingClient,
} from '../api/pricing';

type Scope = 'used' | 'catalog';
type PriceRow = CatalogPricedModel & {
  clients?: Array<Exclude<PricingClient, 'all'>>;
  matched?: boolean;
  last_seen?: string;
};

interface Meta {
  enabled: boolean;
  tableEntries: number;
  totalMatches: number;
}

interface Props {
  refreshKey: number;
}

const PAGE_SIZE = 100;

function formatPrice(value: number | null): string {
  if (value == null) return '—';
  return '$' + (value >= 1 ? value.toFixed(2) : value.toPrecision(2));
}

function formatLastSeen(value?: string): string {
  if (!value) return '—';
  return new Date(value).toLocaleDateString('zh-CN', {
    month: 'numeric',
    day: 'numeric',
  });
}

export function PricingView({ refreshKey }: Props) {
  const [scope, setScope] = useState<Scope>('used');
  const [query, setQuery] = useState('');
  const [prefix, setPrefix] = useState('');
  const [rows, setRows] = useState<PriceRow[]>([]);
  const [meta, setMeta] = useState<Meta | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [loadMoreError, setLoadMoreError] = useState<string | null>(null);
  const requestKey = JSON.stringify([scope, prefix, refreshKey]);
  const [completedRequestKey, setCompletedRequestKey] = useState<string | null>(
    null,
  );
  const loading = completedRequestKey !== requestKey;
  const requestSequence = useRef(0);

  useEffect(() => {
    const timer = window.setTimeout(() => setPrefix(query.trim()), 250);
    return () => window.clearTimeout(timer);
  }, [query]);

  useEffect(() => {
    let cancelled = false;
    const sequence = ++requestSequence.current;

    const request = scope === 'used'
      ? Pricing.used(prefix)
      : Pricing.catalog(prefix, 0, PAGE_SIZE);

    request
      .then(response => {
        if (cancelled || sequence !== requestSequence.current) return;
        setError(null);
        setLoadingMore(false);
        setLoadMoreError(null);
        setRows(response.models);
        setMeta({
          enabled: response.enabled,
          tableEntries: response.table_entries ?? 0,
          totalMatches: 'total_matches' in response
            ? response.total_matches
            : response.models.length,
        });
        setCompletedRequestKey(requestKey);
      })
      .catch(err => {
        if (cancelled || sequence !== requestSequence.current) return;
        setLoadingMore(false);
        setLoadMoreError(null);
        setRows([]);
        setMeta(null);
        setError(String(err));
        setCompletedRequestKey(requestKey);
      });

    return () => {
      cancelled = true;
    };
  }, [prefix, refreshKey, requestKey, scope]);

  const loadMore = () => {
    if (scope !== 'catalog' || loading || loadingMore || !meta) return;
    const sequence = requestSequence.current;
    setLoadingMore(true);
    setLoadMoreError(null);
    Pricing.catalog(prefix, rows.length, PAGE_SIZE)
      .then(response => {
        if (sequence !== requestSequence.current) return;
        setRows(current => [...current, ...response.models]);
        setMeta(current => current
          ? { ...current, totalMatches: response.total_matches }
          : current);
      })
      .catch(err => {
        if (sequence === requestSequence.current) {
          setLoadMoreError(String(err));
        }
      })
      .finally(() => {
        if (sequence === requestSequence.current) setLoadingMore(false);
      });
  };

  const hasMore =
    scope === 'catalog' &&
    !loading &&
    meta != null &&
    rows.length < meta.totalMatches;

  const selectScope = (next: Scope) => {
    if (next === scope) return;
    setLoadingMore(false);
    setError(null);
    setLoadMoreError(null);
    setScope(next);
  };

  return (
    <main className="page">
      <div className="section-head pricing-page-head">
        <div>
          <h2>模型价格</h2>
          <p>LiteLLM 参考单价 · USD / 1M tokens</p>
        </div>
        <div className="range-toggle" aria-label="价格范围">
          <button
            aria-pressed={scope === 'used'}
            onClick={() => selectScope('used')}
          >
            实际使用
          </button>
          <button
            aria-pressed={scope === 'catalog'}
            onClick={() => selectScope('catalog')}
          >
            全部模型
          </button>
        </div>
      </div>

      <section className="card pricing-card">
        <div className="pricing-toolbar">
          <div>
            <h3>{scope === 'used' ? '实际使用过的模型' : '完整模型目录'}</h3>
            <div className="card-sub">
              {!loading && meta
                ? `${meta.totalMatches.toLocaleString()} 个匹配 · 计价表共 ${meta.tableEntries.toLocaleString()} 条`
                : '读取计价表…'}
            </div>
          </div>
          <label className="pricing-search">
            <span>模型前缀</span>
            <input
              type="search"
              value={query}
              onChange={event => setQuery(event.target.value)}
              placeholder="例如 gpt-5.6"
              autoComplete="off"
            />
          </label>
        </div>

        {!loading && error && (
          <div className="pricing-state">加载失败：{error}</div>
        )}
        {loading && <div className="pricing-state">加载中…</div>}
        {!loading && !error && meta && !meta.enabled && (
          <div className="pricing-state">
            在服务端 config.yaml 中开启 <code>pricing.enabled</code> 并配置{' '}
            <code>source_file</code> 后，这里会展示模型单价。
          </div>
        )}
        {!loading && !error && meta?.enabled && rows.length === 0 && (
          <div className="pricing-state">没有匹配此前缀的模型</div>
        )}
        {!loading && !error && meta?.enabled && rows.length > 0 && (
          <>
            <div className="table-scroll">
              <table className="model-table pricing-table">
                <thead>
                  <tr>
                    <th>模型</th>
                    {scope === 'used' && <th>客户端</th>}
                    <th className="num">输入</th>
                    <th className="num">输出</th>
                    <th className="num">缓存读</th>
                    <th className="num">推理输出</th>
                    {scope === 'used' && <th className="num">最近使用</th>}
                  </tr>
                </thead>
                <tbody>
                  {rows.map(row => (
                    <tr key={row.model}>
                      <td>
                        <div className="pricing-model-name">
                          {row.model}
                          {scope === 'used' && row.matched === false && (
                            <span className="unmatched-tag">未收录</span>
                          )}
                        </div>
                      </td>
                      {scope === 'used' && (
                        <td>
                          {(row.clients ?? []).map(client => (
                            <span
                              key={client}
                              className={client === 'codex'
                                ? 'client-badge client-badge--codex'
                                : 'client-badge'}
                            >
                              {client === 'codex' ? 'Codex' : 'Claude'}
                            </span>
                          ))}
                        </td>
                      )}
                      <td className="num">{formatPrice(row.input_per_1m)}</td>
                      <td className="num">{formatPrice(row.output_per_1m)}</td>
                      <td className="num">{formatPrice(row.cache_read_per_1m)}</td>
                      <td className="num">{formatPrice(row.reasoning_output_per_1m)}</td>
                      {scope === 'used' && (
                        <td className="num">{formatLastSeen(row.last_seen)}</td>
                      )}
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            <div className="pricing-footer">
              <span>
                已显示 {rows.length.toLocaleString()} / {meta.totalMatches.toLocaleString()}
              </span>
              <div className="pricing-load-more">
                {loadMoreError && (
                  <span className="pricing-load-more-error" role="alert">
                    加载更多失败：{loadMoreError}
                  </span>
                )}
                {hasMore && (
                  <button
                    className="load-more-btn"
                    disabled={loadingMore}
                    onClick={loadMore}
                  >
                    {loadingMore
                      ? '加载中…'
                      : loadMoreError
                        ? '重试加载'
                        : `再加载 ${PAGE_SIZE} 条`}
                  </button>
                )}
              </div>
            </div>
            <div className="card-sub">
              注：Claude 实际成本以客户端自报为准，此表仅为参考单价；“未收录”表示该模型不在当前计价表中。
            </div>
          </>
        )}
      </section>
    </main>
  );
}
