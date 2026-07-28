import { useEffect, useState } from 'react';
import { Icon } from './components/Icon';
import { Sparkline } from './components/charts/Sparkline';
import { StackedAreaChart } from './components/charts/StackedAreaChart';
import type { ChartSeries } from './components/charts/StackedAreaChart';
import { LineChart } from './components/charts/LineChart';
import { DonutChart } from './components/charts/DonutChart';
import { CalendarHeatmap } from './components/charts/CalendarHeatmap';
import { TweaksPanel, useTweaks } from './components/TweaksPanel';
import { Dashboard } from './api/dashboard';
import type { DashboardData, Range, Since, Client } from './api/dashboard';
import { formatCurrency, formatPct, formatTokens } from './lib/format';
import { TOOL_PALETTE, SKILL_PALETTE } from './lib/palette';
import { SessionsView } from './views/SessionsView';
import { SessionDetailView } from './views/SessionDetailView';

function useAnimated(target: number, duration = 700): number {
  const [val, setVal] = useState(0);
  useEffect(() => {
    let raf = 0;
    const start = performance.now();
    const from = 0;
    const tick = (t: number) => {
      const p = Math.min(1, (t - start) / duration);
      const eased = 1 - Math.pow(1 - p, 3);
      setVal(from + (target - from) * eased);
      if (p < 1) raf = requestAnimationFrame(tick);
    };
    raf = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(raf);
  }, [target, duration]);
  return val;
}

interface KpiCardProps {
  icon: 'coins' | 'dollar' | 'database' | 'activity';
  label: string;
  value: number | string;
  unit?: string;
  delta?: number;
  foot: React.ReactNode;
  sparkValues?: number[];
  sparkColor?: string;
  animate?: boolean;
  /**
   * Decimal places to display. If omitted: 0 for integer values, 2 otherwise.
   * Pass explicitly when the source is a float that should round to a fixed
   * precision (e.g. USD = 2, percentages = 1).
   */
  precision?: number;
}

function KpiCard({ icon, label, value, unit, delta, foot, sparkValues, sparkColor, animate = false, precision }: KpiCardProps) {
  const num = typeof value === 'number' ? value : 0;
  const animated = useAnimated(num, animate ? 700 : 0);
  let displayValue: string;
  if (typeof value === 'number') {
    const current = animate ? animated : num;
    const digits = precision ?? (Number.isInteger(value) ? 0 : 2);
    displayValue = current.toLocaleString('en-US', {
      minimumFractionDigits: digits,
      maximumFractionDigits: digits,
    });
  } else {
    displayValue = value;
  }

  return (
    <div className="kpi">
      <div className="kpi__top">
        <div className="kpi__label">
          <span className="kpi__icon">
            <Icon name={icon} size={16} />
          </span>
          <span>{label}</span>
        </div>
        {delta != null && (
          <span className={`kpi__delta ${delta >= 0 ? 'up' : 'down'}`}>
            {delta >= 0 ? '↑' : '↓'} {Math.abs(delta).toFixed(1)}%
          </span>
        )}
      </div>
      <div className="kpi__value">
        <span>{displayValue}</span>
        {unit && <span className="kpi__unit">{unit}</span>}
      </div>
      <div className="kpi__foot">
        <span>{foot}</span>
        {sparkValues && <Sparkline values={sparkValues} color={sparkColor || 'var(--accent)'} />}
      </div>
    </div>
  );
}

// Client-side view selector (no router): the dashboard, the session list, or
// one session's detail. Navigation is plain state — URLs are not deep-linkable
// in this first cut.
type View =
  | { name: 'dashboard' }
  | { name: 'sessions' }
  | { name: 'session'; id: string; client: 'claude' | 'codex' };

export default function App() {
  const [tweaks, setTweak] = useTweaks();
  const [range, setRange] = useState<Range>('day');
  const [since, setSince] = useState<Since>('7d');
  const [client, setClient] = useState<Client>('all');
  const [data, setData] = useState<DashboardData | null>(null);
  // seriesOn keys are model group ids (e.g. "opus-4.7", "deepseek-v3").
  // Missing entries default to enabled — newly seen groups light up on first load.
  const [seriesOn, setSeriesOn] = useState<Record<string, boolean>>({});
  const [refreshing, setRefreshing] = useState(false);
  const [now, setNow] = useState(new Date());
  const [refreshKey, setRefreshKey] = useState(0);
  const [view, setView] = useState<View>({ name: 'dashboard' });

  useEffect(() => {
    let cancelled = false;
    setRefreshing(true);
    Dashboard.fetch(range, since, client)
      .then(d => {
        if (!cancelled) {
          setData(d);
          window.setTimeout(() => setRefreshing(false), 300);
        }
      })
      .catch(err => {
        if (!cancelled) {
          console.error('dashboard fetch failed', err);
          setRefreshing(false);
        }
      });
    return () => {
      cancelled = true;
    };
  }, [range, since, refreshKey, client]);

  useEffect(() => {
    const i = window.setInterval(() => setNow(new Date()), 60_000);
    return () => window.clearInterval(i);
  }, []);

  // Auto-refresh aligned to wall-clock 5-minute marks (xx:00, xx:05, ...).
  // Re-uses the manual refresh path by bumping refreshKey; the fetch
  // effect above handles the actual reload. Self-scheduling setTimeout is
  // preferred over setInterval here because each tick recomputes the gap
  // to the next boundary — drift from suspend/resume or browser throttling
  // self-corrects on the following tick.
  useEffect(() => {
    let timer: number | undefined;
    const scheduleNext = () => {
      const now = new Date();
      const msSinceBoundary =
        (now.getMinutes() % 5) * 60_000 +
        now.getSeconds() * 1000 +
        now.getMilliseconds();
      const delay = 5 * 60_000 - msSinceBoundary;
      timer = window.setTimeout(() => {
        setRefreshKey(k => k + 1);
        scheduleNext();
      }, delay);
    };
    scheduleNext();
    return () => {
      if (timer !== undefined) window.clearTimeout(timer);
    };
  }, []);

  // renderShell wraps any view body with the shared header (brand + nav +
  // clock + refresh + theme) and the tweaks panel, so every view keeps the
  // same chrome without duplicating it.
  const renderShell = (children: React.ReactNode) => (
    <div className="app">
      <header className="app-header">
        <div className="app-header__inner">
          <a
            className="brand"
            href="#"
            onClick={e => {
              e.preventDefault();
              setView({ name: 'dashboard' });
            }}
          >
            <span className="brand__logo">C</span>
            <span className="brand__name">Claude Code Monitor</span>
          </a>
          <nav className="app-nav">
            <button
              data-on={view.name === 'dashboard'}
              onClick={() => setView({ name: 'dashboard' })}
            >
              仪表盘
            </button>
            <button
              data-on={view.name !== 'dashboard'}
              onClick={() => setView({ name: 'sessions' })}
            >
              会话
            </button>
          </nav>
          <span className="spacer" />
          <span className="live-dot">
            实时同步 ·{' '}
            {now.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' })}
          </span>
          <button
            className="icon-btn"
            title="刷新"
            onClick={() => setRefreshKey(k => k + 1)}
            style={{ animation: refreshing ? 'spin 600ms linear' : 'none' }}
          >
            <Icon name="refresh" size={15} />
          </button>
          <button
            className="icon-btn"
            title={tweaks.dark ? '切换浅色' : '切换深色'}
            onClick={() => setTweak('dark', !tweaks.dark)}
          >
            <Icon name={tweaks.dark ? 'sun' : 'moon'} size={15} />
          </button>
        </div>
      </header>

      {children}

      <TweaksPanel tweaks={tweaks} setTweak={setTweak} />
    </div>
  );

  if (view.name === 'sessions') {
    return renderShell(
      <SessionsView
        client={client}
        onOpen={(id, c) => setView({ name: 'session', id, client: c })}
      />
    );
  }
  if (view.name === 'session') {
    return renderShell(
      <SessionDetailView
        id={view.id}
        client={view.client}
        onBack={() => setView({ name: 'sessions' })}
      />
    );
  }

  if (!data) {
    return renderShell(
      <main className="page">
        <div className="card">加载中…</div>
      </main>
    );
  }

  const trendGroups = data.series.groups;
  const chartSeries: ChartSeries[] = trendGroups.map(m => ({
    id: m.id,
    label: m.label,
    color: m.color,
    on: seriesOn[m.id] !== false, // undefined → on
  }));

  const seriesTotals = trendGroups.map(m => ({
    ...m,
    total: data.series.points.reduce((s, p) => s + (p.values[m.id] ?? 0), 0),
  }));

  const totalToolUses = data.tools.reduce((s, x) => s + x.count, 0);
  const totalSkillActivations = data.skills.reduce((s, x) => s + x.activations, 0);

  const toolsForDonut = data.tools.map((d, i) => ({
    name: d.name,
    value: d.count,
    color: TOOL_PALETTE[i] || 'var(--fg-3)',
  }));
  const skillsForDonut = data.skills.map((d, i) => ({
    name: d.name,
    value: d.activations,
    color: SKILL_PALETTE[i % SKILL_PALETTE.length],
  }));

  const RANGE_PREFIX: Record<Range, string> = { day: '今日', week: '本周', month: '本月' };
  const PREV_LABEL: Record<Range, string> = { day: '昨日', week: '上周', month: '上月' };
  const rangePrefix = RANGE_PREFIX[range];
  const prevLabel = PREV_LABEL[range];

  const pctDelta = (cur: number, prev: number): number | undefined => {
    if (prev <= 0) return undefined;
    return ((cur - prev) / prev) * 100;
  };
  const tokenDelta = pctDelta(data.tokens.total, data.tokens.prev_total);
  const costDelta = pctDelta(data.cost.total, data.cost.prev_total);
  const requestsDelta = pctDelta(data.requests.total, data.requests.prev_total);

  const RATES_WINDOW_LABEL: Record<Range, string> = {
    day: '近 48 小时 · 每小时',
    week: '近 7 天 · 每 6 小时',
    month: '近 30 天 · 每天',
  };
  const THROUGHPUT_META: Record<string, { label: string; color: string }> = {
    input: { label: '输入', color: '#3B6FD4' },
    output: { label: '输出', color: '#D97757' },
    cache_read: { label: '缓存读', color: '#7B4E9A' },
    cache_creation: { label: '缓存写', color: '#D4860A' },
  };

  const speedSeries: ChartSeries[] = data.rates.speed.groups.map(m => ({
    id: m.id,
    label: m.label,
    color: m.color,
    on: true,
  }));
  const throughputSeries: ChartSeries[] = data.rates.throughput.types.map(t => ({
    id: t,
    label: THROUGHPUT_META[t]?.label ?? t,
    color: THROUGHPUT_META[t]?.color ?? 'var(--fg-3)',
    on: true,
  }));
  const speedCur = data.rates.speed.current;
  const speedPrev = data.rates.speed.previous;
  const speedDelta =
    speedCur != null && speedPrev != null && speedPrev > 0
      ? ((speedCur - speedPrev) / speedPrev) * 100
      : undefined;

  // 单价格式化:>=1 保留两位;<1 保留两位有效数字(如 $0.075);null → —
  const fmtPrice = (v: number | null): string => {
    if (v == null) return '—';
    return '$' + (v >= 1 ? v.toFixed(2) : v.toPrecision(2));
  };

  return renderShell(
    <main className="page">
        <div className="page-hero">
          <div>
            <h1>
              {rangePrefix}的{' '}
              <em>{client === 'codex' ? 'Codex' : client === 'claude' ? 'Claude Code' : 'AI 编码'}</em>{' '}
              表现
            </h1>
            <p>
              {now.toLocaleDateString('zh-CN', {
                year: 'numeric',
                month: 'long',
                day: 'numeric',
                weekday: 'long',
              })}{' '}
              · 数据每分钟自动同步
            </p>
          </div>
          <div className="hero-toggles">
            <div className="range-toggle" role="tablist" aria-label="客户端">
              {(
                [
                  ['all', '全部'],
                  ['claude', 'Claude'],
                  ['codex', 'Codex'],
                ] as const
              ).map(([k, l]) => (
                <button key={k} aria-pressed={client === k} onClick={() => setClient(k)}>
                  {l}
                </button>
              ))}
            </div>
            <div className="range-toggle" role="tablist" aria-label="时间维度">
              {(
                [
                  ['day', '日'],
                  ['week', '周'],
                  ['month', '月'],
                ] as const
              ).map(([k, l]) => (
                <button key={k} aria-pressed={range === k} onClick={() => setRange(k)}>
                  {l}
                </button>
              ))}
            </div>
          </div>
        </div>

        <div className="kpi-grid">
          <KpiCard
            icon="coins"
            label={`${rangePrefix} Token 用量`}
            value={data.tokens.total}
            delta={tokenDelta}
            foot={
              <>
                输入 <strong>{formatTokens(data.tokens.in)}</strong> · 输出{' '}
                <strong>{formatTokens(data.tokens.out)}</strong>
              </>
            }
            sparkValues={data.tokens.sparkline}
            animate
          />
          {(client !== 'codex' || data.cost.cost_estimated) && (
            <KpiCard
              icon="dollar"
              label={`${rangePrefix}消费金额${data.cost.cost_estimated ? '(含估算)' : ''}`}
              value={data.cost.total}
              unit="USD"
              delta={costDelta}
              precision={2}
              foot={
                <>
                  {prevLabel} <strong>${data.cost.prev_total.toFixed(2)}</strong>
                </>
              }
              sparkValues={data.cost.sparkline}
              animate
            />
          )}
          <KpiCard
            icon="database"
            label={`${rangePrefix}缓存命中率`}
            value={
              data.cache.hit_rate == null
                ? 'N/A'
                : Number((data.cache.hit_rate * 100).toFixed(1))
            }
            unit={data.cache.hit_rate == null ? undefined : '%'}
            foot={
              <div style={{ width: '100%' }}>
                <div className="cache-meter__bar" style={{ marginTop: 2 }}>
                  <i
                    className="hit"
                    style={{ width: (data.cache.hit_rate ?? 0) * 100 + '%' }}
                  />
                  <i className="miss" style={{ flex: 1 }} />
                </div>
                <div
                  style={{
                    display: 'flex',
                    justifyContent: 'space-between',
                    fontSize: 11,
                    marginTop: 6,
                    color: 'var(--fg-3)',
                  }}
                >
                  <span>命中 {formatTokens(data.cache.read_tokens)}</span>
                  <span>创建 {formatTokens(data.cache.creation_tokens)}</span>
                </div>
              </div>
            }
          />
          <KpiCard
            icon="activity"
            label={`${rangePrefix}请求次数`}
            value={data.requests.total}
            delta={requestsDelta}
            foot={
              <>
                {prevLabel} <strong>{data.requests.prev_total.toLocaleString()}</strong> 次
              </>
            }
            sparkValues={data.requests.sparkline}
            sparkColor="var(--accent)"
            animate
          />
        </div>

        <section className="card">
          <div className="card-head">
            <div>
              <h3>用量热点图</h3>
              <div className="card-sub">
                最近 360 天 · 综合用量（Token×{data.heatmap.weights.tokens} / 费用×
                {data.heatmap.weights.cost} / 请求×{data.heatmap.weights.requests}）
              </div>
            </div>
            <div className="heatmap-legend">
              <span>少</span>
              <i style={{ background: 'var(--bg-inset)' }} />
              <i style={{ background: 'var(--color-orange-200)' }} />
              <i style={{ background: 'var(--color-orange-300)' }} />
              <i style={{ background: 'var(--color-orange-400)' }} />
              <i style={{ background: 'var(--color-orange-600)' }} />
              <span>多</span>
            </div>
          </div>
          <CalendarHeatmap days={data.heatmap.points} />
        </section>

        <section className="card">
          <div className="card-head">
            <div>
              <h3>各模型 Token 用量趋势</h3>
              <div className="card-sub">
                趋势对比 ·{' '}
                {range === 'day' ? '近 14 天' : range === 'week' ? '近 12 周' : '近 12 个月'}
              </div>
            </div>
          </div>

          <div className="trends__legend">
            {trendGroups.map(m => {
              const id = m.id;
              const total = seriesTotals.find(s => s.id === id)?.total ?? 0;
              const on = seriesOn[id] !== false;
              return (
                <button
                  key={id}
                  className="legend-chip"
                  data-on={on}
                  onClick={() => setSeriesOn(s => ({ ...s, [id]: !on }))}
                >
                  <span className="legend-chip__dot" style={{ background: m.color }} />
                  <span>{m.label}</span>
                  <span className="legend-chip__val">{formatTokens(total)}</span>
                </button>
              );
            })}
          </div>

          <div className="trends">
            <StackedAreaChart points={data.series.points} series={chartSeries} />
          </div>
        </section>

        <div className="section-head">
          <div>
            <h2>生产速率</h2>
            <p>
              {RATES_WINDOW_LABEL[range]} ·{' '}
              {client === 'codex'
                ? '生成速度按 Codex 原生 TBT 换算'
                : client === 'claude'
                  ? '生成速度按请求耗时加权'
                  : 'Claude 按请求耗时 · Codex 按原生 TBT'}{' '}
              · 吞吐率按墙钟时间归一
            </p>
          </div>
        </div>

        <div className="cols-2">
          <section className="card">
            <div className="card-head">
              <div>
                <h3>生成速度</h3>
                <div className="card-sub">
                  {client === 'codex'
                    ? '1000 / 平均 TBT · 按模型分组'
                    : client === 'claude'
                      ? 'output tokens / 请求耗时 · 按模型分组'
                      : '按客户端原生口径 · 按模型分组'}
                </div>
              </div>
              <div style={{ textAlign: 'right' }}>
                <div className="kpi__value" style={{ fontSize: 22 }}>
                  <span>{speedCur == null ? '—' : speedCur.toFixed(1)}</span>
                  <span className="kpi__unit">tok/s</span>
                </div>
                {speedDelta != null && (
                  <span className={`kpi__delta ${speedDelta >= 0 ? 'up' : 'down'}`}>
                    {speedDelta >= 0 ? '↑' : '↓'} {Math.abs(speedDelta).toFixed(1)}% vs 前一窗口
                  </span>
                )}
              </div>
            </div>
            <div className="trends__legend">
              {data.rates.speed.groups.map(m => (
                <span key={m.id} className="legend-chip" data-on>
                  <span className="legend-chip__dot" style={{ background: m.color }} />
                  <span>{m.label}</span>
                </span>
              ))}
            </div>
            <LineChart points={data.rates.speed.points} series={speedSeries} height={240} />
            <div className="card-sub">
              {client === 'codex'
                ? '注：TBT 是相邻生成 token 的平均间隔；数值越高表示纯解码越快。'
                : client === 'claude'
                  ? '注：耗时含首 token 等待，数值略低于纯解码速度；失败请求不计入。'
                  : '注：两家速度口径不同，混合视图不合并顶部 KPI；切换客户端可查看单一口径。'}
            </div>
          </section>

          <section className="card">
            <div className="card-head">
              <div>
                <h3>吞吐率</h3>
                <div className="card-sub">tokens / 分钟 · 按 token 类型</div>
              </div>
              <div className="trends__legend">
                {throughputSeries.map(s => (
                  <span key={s.id} className="legend-chip" data-on>
                    <span className="legend-chip__dot" style={{ background: s.color }} />
                    <span>{s.label}</span>
                  </span>
                ))}
              </div>
            </div>
            <StackedAreaChart
              points={data.rates.throughput.points}
              series={throughputSeries}
              height={240}
            />
          </section>
        </div>

        <div className="section-head">
          <div>
            <h2>累计排名</h2>
            <p>
              {since === 'all' ? '所有时间' : `近 ${since === '7d' ? '7 天' : '30 天'}`} · 跨项目汇总 ·{' '}
              {client === 'codex'
                ? '仅统计 Codex'
                : client === 'claude'
                  ? '仅统计 Claude Code'
                  : 'Claude Code + Codex'}
            </p>
          </div>
          <div className="range-toggle" role="tablist" aria-label="排名时段">
            {(
              [
                ['7d', '7 天'],
                ['30d', '30 天'],
                ['all', '全部'],
              ] as const
            ).map(([k, l]) => (
              <button key={k} aria-pressed={since === k} onClick={() => setSince(k)}>
                {l}
              </button>
            ))}
          </div>
        </div>

        <div className="cols-2">
          <section className="card">
            <div className="card-head">
              <div>
                <h3>工具调用次数</h3>
                <div className="card-sub">{totalToolUses.toLocaleString()} 次累计</div>
              </div>
            </div>
            <div className="rank">
              <DonutChart
                data={toolsForDonut}
                centerLabel="次工具调用"
                centerValue={formatTokens(totalToolUses).replace('.0', '')}
              />
              <div className="rank-list">
                {data.tools.slice(0, 6).map((tool, i) => (
                  <div className="rank-list__row" key={tool.name}>
                    <span className="rank-list__dot" style={{ background: TOOL_PALETTE[i] }} />
                    <span className="rank-list__name">{tool.name}</span>
                    <span className="rank-list__count">{tool.count.toLocaleString()}</span>
                    <span className="rank-list__pct">
                      {formatPct(tool.count / totalToolUses, 1)}
                    </span>
                  </div>
                ))}
              </div>
            </div>
          </section>

          <section className="card">
            <div className="card-head">
              <div>
                <h3>Skill 激活次数</h3>
                <div className="card-sub">{totalSkillActivations.toLocaleString()} 次累计</div>
              </div>
            </div>
            <div className="rank">
              <DonutChart
                data={skillsForDonut}
                centerLabel="次激活"
                centerValue={totalSkillActivations.toLocaleString()}
              />
              <div className="rank-list">
                {data.skills.slice(0, 6).map((sk, i) => (
                  <div className="rank-list__row" key={sk.name}>
                    <span
                      className="rank-list__dot"
                      style={{ background: SKILL_PALETTE[i % SKILL_PALETTE.length] }}
                    />
                    <span className="rank-list__name">{sk.name}</span>
                    <span className="rank-list__count">{sk.activations}</span>
                    <span className="rank-list__pct">
                      {formatPct(sk.activations / totalSkillActivations, 1)}
                    </span>
                  </div>
                ))}
              </div>
            </div>
          </section>
        </div>

        <section className="card">
          <div className="card-head">
            <div>
              <h3>模型用量明细</h3>
              <div className="card-sub">累计请求 / Tokens / 费用</div>
            </div>
          </div>
          <table className="model-table">
            <thead>
              <tr>
                <th>模型</th>
                <th className="num">请求数</th>
                <th className="num">输入 Tokens</th>
                <th className="num">输出 Tokens</th>
                <th className="num">缓存读取</th>
                {(client !== 'codex' || data.cost.cost_estimated) && <th className="num">费用</th>}
                <th className="num" style={{ minWidth: 140 }}>
                  占比
                </th>
              </tr>
            </thead>
            <tbody>
              {data.models.map(m => (
                <tr key={m.id}>
                  <td>
                    <div className="model-cell">
                      <div className="model-swatch" style={{ background: m.color }} />
                      <div>
                        <div className="model-name">{m.label}</div>
                        <div className="model-tier">{m.tier}</div>
                      </div>
                    </div>
                  </td>
                  <td className="num">{m.requests.toLocaleString()}</td>
                  <td className="num">{formatTokens(m.tokens_in)}</td>
                  <td className="num">{formatTokens(m.tokens_out)}</td>
                  <td className="num">{formatTokens(m.cache_tokens)}</td>
                  {(client !== 'codex' || data.cost.cost_estimated) && (
                    <td className="num">
                      <strong>{formatCurrency(m.cost)}</strong>
                    </td>
                  )}
                  <td className="num">
                    <div className="bar-cell">
                      <span>{formatPct(m.share, 1)}</span>
                      <span className="bar">
                        <i style={{ width: m.share * 100 + '%', background: m.color }} />
                      </span>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </section>

        <section className="card">
          <div className="card-head">
            <div>
              <h3>模型价目表</h3>
              <div className="card-sub">
                {data.pricing.enabled
                  ? `实际使用过的模型 · 单价 $ / 1M tokens · LiteLLM 计价表（${data.pricing.tableEntries.toLocaleString()} 条）`
                  : '未启用成本估算'}
              </div>
            </div>
          </div>
          {data.pricing.enabled ? (
            <>
              <table className="model-table">
                <thead>
                  <tr>
                    <th>模型</th>
                    <th>客户端</th>
                    <th className="num">输入</th>
                    <th className="num">输出</th>
                    <th className="num">缓存读</th>
                    <th className="num">推理输出</th>
                    <th className="num">最近使用</th>
                  </tr>
                </thead>
                <tbody>
                  {data.pricing.models.map(m => (
                    <tr key={m.model}>
                      <td>
                        <div className="model-name" style={{ fontFamily: 'var(--font-mono)', fontSize: 12.5 }}>
                          {m.model}
                          {!m.matched && (
                            <span className="model-tier" style={{ display: 'inline', marginLeft: 8 }}>
                              未收录
                            </span>
                          )}
                        </div>
                      </td>
                      <td>
                        {m.clients.map(c => (
                          <span
                            key={c}
                            className={c === 'codex' ? 'client-badge client-badge--codex' : 'client-badge'}
                          >
                            {c === 'codex' ? 'Codex' : 'Claude'}
                          </span>
                        ))}
                      </td>
                      <td className="num">{fmtPrice(m.input_per_1m)}</td>
                      <td className="num">{fmtPrice(m.output_per_1m)}</td>
                      <td className="num">{fmtPrice(m.cache_read_per_1m)}</td>
                      <td className="num">{fmtPrice(m.reasoning_output_per_1m)}</td>
                      <td className="num">
                        {new Date(m.last_seen).toLocaleDateString('zh-CN', {
                          month: 'numeric',
                          day: 'numeric',
                        })}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
              <div className="card-sub">
                注：Claude 实际成本以客户端自报为准，此表仅为参考单价；「未收录」表示该模型不在计价表中。
              </div>
            </>
          ) : (
            <div className="card-sub">
              在服务端 config.yaml 中开启 <code>pricing.enabled</code> 并配置{' '}
              <code>source_file</code>（参考 config.example.yaml）后，这里会展示实际使用过的模型单价。
            </div>
          )}
        </section>

      </main>
  );
}
