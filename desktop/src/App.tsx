/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 3.0.0
 */

import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
} from 'react'

import {
  defaultConnectionSettings,
  tauriMonitorApi,
  type ConnectionSettings,
  type DashboardPayload,
  type MonitorApi,
  type PeriodModel,
  type TelemetryClient,
  type ThemeMode,
  type UsageRange,
} from './monitorApi'
import { SegmentedControl } from './components/SegmentedControl'
import { SettingsView } from './components/SettingsView'

const REFRESH_INTERVAL_MS = 5 * 60 * 1_000
const clientOptions: Array<{ value: TelemetryClient; label: string }> = [
  { value: 'all', label: '全部' },
  { value: 'claude', label: 'Claude' },
  { value: 'codex', label: 'Codex' },
]
const rangeOptions: Array<{ value: UsageRange; label: string }> = [
  { value: 'day', label: '今日' },
  { value: 'week', label: '本周' },
  { value: 'month', label: '本月' },
]

type ConnectionState = 'idle' | 'loading' | 'connected' | 'stale' | 'offline'
type View = 'dashboard' | 'settings'

interface AppProps {
  api?: MonitorApi
}

interface ScopedConnectionStatus {
  scope: string
  connection: ConnectionState
  error: string
}

export default function App({ api = tauriMonitorApi }: AppProps) {
  const [settings, setSettings] = useState<ConnectionSettings>(defaultConnectionSettings)
  const [theme, setTheme] = useState<ThemeMode>('system')
  const [themePreview, setThemePreview] = useState<ThemeMode | null>(null)
  const [range, setRange] = useState<UsageRange>('day')
  const [client, setClient] = useState<TelemetryClient>('all')
  const [dataByScope, setDataByScope] = useState<ReadonlyMap<string, DashboardPayload>>(
    () => new Map(),
  )
  const [scopedStatus, setScopedStatus] = useState<ScopedConnectionStatus | null>(null)
  const [panelVisible, setPanelVisible] = useState(false)
  const [view, setView] = useState<View>('dashboard')
  const dataCache = useRef(new Map<string, DashboardPayload>())
  const requestId = useRef(0)
  const scope = dashboardScope(settings, range, client)
  const data = dataByScope.get(scope) ?? null
  const connection: ConnectionState = scopedStatus?.scope === scope
    ? scopedStatus.connection
    : 'idle'
  const error = scopedStatus?.scope === scope ? scopedStatus.error : ''
  const activeTheme = themePreview ?? theme

  const refresh = useCallback(async () => {
    const currentRequest = ++requestId.current
    const cached = dataCache.current.get(scope)
    setScopedStatus((current) => ({
      scope,
      connection: cached
        ? current?.scope === scope ? current.connection : 'connected'
        : 'loading',
      error: '',
    }))
    try {
      const next = await api.fetchDashboard(settings, range, client)
      if (currentRequest !== requestId.current) return
      dataCache.current.set(scope, next)
      setDataByScope((current) => {
        const updated = new Map(current)
        updated.set(scope, next)
        return updated
      })
      setScopedStatus({ scope, connection: 'connected', error: '' })
    } catch (reason) {
      if (currentRequest !== requestId.current) return
      setScopedStatus({
        scope,
        connection: cached ? 'stale' : 'offline',
        error: errorMessage(reason),
      })
    }
  }, [api, client, range, scope, settings])

  useEffect(() => {
    if (activeTheme === 'system') {
      delete document.documentElement.dataset.theme
    } else {
      document.documentElement.dataset.theme = activeTheme
    }
  }, [activeTheme])

  const openSettings = useCallback(() => {
    setThemePreview(theme)
    setView('settings')
  }, [theme])

  const cancelSettings = useCallback(() => {
    setThemePreview(null)
    setView('dashboard')
  }, [])

  useEffect(() => {
    let disposed = false
    const unlisten: Array<() => void> = []
    void api
      .isPanelVisible()
      .then((visible) => {
        if (!disposed) setPanelVisible(visible)
      })
      .catch(() => {
        if (!disposed) setPanelVisible(true)
      })
    void Promise.all([
      api.listen('panel-shown', () => setPanelVisible(true)),
      api.listen('panel-hidden', () => setPanelVisible(false)),
      api.listen('refresh-requested', () => void refresh()),
      api.listen('settings-requested', openSettings),
    ]).then((listeners) => {
      if (disposed) listeners.forEach((stop) => stop())
      else unlisten.push(...listeners)
    })

    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') void api.hidePanel()
    }
    window.addEventListener('keydown', onKeyDown)
    return () => {
      disposed = true
      unlisten.forEach((stop) => stop())
      window.removeEventListener('keydown', onKeyDown)
    }
  }, [api, openSettings, refresh])

  useEffect(() => {
    if (!panelVisible) return
    const initialRefresh = window.setTimeout(() => void refresh(), 0)
    const interval = window.setInterval(() => void refresh(), REFRESH_INTERVAL_MS)
    return () => {
      window.clearTimeout(initialRefresh)
      window.clearInterval(interval)
    }
  }, [panelVisible, refresh])

  const applySettings = useCallback(
    async (nextSettings: ConnectionSettings, nextTheme: ThemeMode, autostart: boolean) => {
      await api.setAutostartEnabled(autostart)
      setSettings(nextSettings)
      setTheme(nextTheme)
      setThemePreview(null)
      setView('dashboard')
    },
    [api],
  )

  return (
    <main className="panel-shell">
      <div className="panel-arrow" aria-hidden="true" />
      <section className="panel" aria-label="Vibecoding Monitor">
        {view === 'settings' ? (
          <SettingsView
            api={api}
            settings={settings}
            theme={activeTheme}
            onBack={cancelSettings}
            onThemePreview={setThemePreview}
            onApply={applySettings}
          />
        ) : (
          <>
            <PanelHeader
              connection={connection}
              onRefresh={() => void refresh()}
              onSettings={openSettings}
            />
            <div className="panel-scroll">
              <div className="filters" aria-label="数据筛选">
                <SegmentedControl
                  ariaLabel="客户端"
                  options={clientOptions}
                  value={client}
                  onChange={setClient}
                />
                <SegmentedControl
                  ariaLabel="统计周期"
                  options={rangeOptions}
                  value={range}
                  onChange={setRange}
                />
              </div>
              {data ? (
                <Dashboard data={data} connection={connection} />
              ) : (
                <EmptyState
                  loading={connection === 'idle' || connection === 'loading'}
                  error={error}
                  onRetry={() => void refresh()}
                  onSettings={openSettings}
                />
              )}
            </div>
            <PanelFooter data={data} connection={connection} settings={settings} />
          </>
        )}
      </section>
    </main>
  )
}

function PanelHeader({
  connection,
  onRefresh,
  onSettings,
}: {
  connection: ConnectionState
  onRefresh: () => void
  onSettings: () => void
}) {
  return (
    <header className="panel-header">
      <div className="brand">
        <LogoMark />
        <div>
          <p className="eyebrow">VIBECODING MONITOR</p>
          <h1>Token 用量</h1>
        </div>
      </div>
      <div className="header-actions">
        <button
          className={`icon-button ${connection === 'loading' ? 'is-spinning' : ''}`}
          type="button"
          aria-label="立即刷新"
          title="立即刷新"
          onClick={onRefresh}
        >
          <RefreshIcon />
        </button>
        <button
          className="icon-button"
          type="button"
          aria-label="打开设置"
          title="设置"
          onClick={onSettings}
        >
          <SettingsIcon />
        </button>
      </div>
    </header>
  )
}

function Dashboard({
  data,
  connection,
}: {
  data: DashboardPayload
  connection: ConnectionState
}) {
  const { snapshot } = data
  const visibleModels = useMemo(() => foldModels(data.models.models), [data.models.models])
  return (
    <div className="dashboard-content">
      {connection === 'stale' && (
        <div className="stale-banner" role="status">
          <WarningIcon />
          <span>连接中断，正在展示最后一次成功数据</span>
        </div>
      )}
      <section className="hero-grid" aria-label="用量概览">
        <article className="hero-card token-card">
          <div className="metric-heading">
            <span>Token 总量</span>
            <ChangeBadge current={snapshot.tokens.total} previous={snapshot.tokens.prev_total} />
          </div>
          <strong>{formatTokens(snapshot.tokens.total)}</strong>
          <p>
            输入 {formatTokens(snapshot.tokens.in)} · 输出 {formatTokens(snapshot.tokens.out)}
          </p>
          <Sparkline values={snapshot.tokens.sparkline} />
        </article>
        <article className="hero-card cost-card">
          <div className="metric-heading">
            <span>费用</span>
            {snapshot.cost.cost_estimated && <em>含估算</em>}
          </div>
          <strong>${snapshot.cost.total.toFixed(2)}</strong>
          <p>上期 ${snapshot.cost.prev_total.toFixed(2)}</p>
          <Sparkline values={snapshot.cost.sparkline} />
        </article>
      </section>

      <section className="mini-grid" aria-label="效率指标">
        <MiniMetric
          label="缓存命中"
          value={snapshot.cache.hit_rate === null ? 'N/A' : `${(snapshot.cache.hit_rate * 100).toFixed(0)}%`}
          detail={`${formatTokens(snapshot.cache.read_tokens)} 已读取`}
        />
        <MiniMetric
          label="请求次数"
          value={snapshot.requests.total.toLocaleString('zh-CN')}
          detail={comparisonText(snapshot.requests.total, snapshot.requests.prev_total)}
        />
      </section>

      <section className="model-section" aria-labelledby="models-title">
        <div className="section-heading">
          <div>
            <p className="section-kicker">MODEL MIX</p>
            <h2 id="models-title">模型用量</h2>
          </div>
          <span>{data.models.models.length} 个模型</span>
        </div>
        <div className="model-list">
          {visibleModels.length === 0 ? (
            <p className="no-models">
              {data.models.available === false
                ? '当前服务版本不支持周期模型统计，请重启升级后的后端'
                : '当前周期暂无模型数据'}
            </p>
          ) : (
            visibleModels.map((model, index) => (
              <ModelRow key={model.model} model={model} index={index} />
            ))
          )}
        </div>
      </section>
    </div>
  )
}

function ModelRow({ model, index }: { model: PeriodModel; index: number }) {
  const style = {
    '--model-color': `var(--model-${Math.min(index + 1, 4)})`,
    '--model-share': `${Math.min(model.share * 100, 100)}%`,
  } as CSSProperties
  return (
    <article className="model-row" style={style}>
      <div className="model-rank">{index + 1}</div>
      <div className="model-body">
        <div className="model-title-row">
          <strong>{model.model}</strong>
          <span>{(model.share * 100).toFixed(1)}%</span>
        </div>
        <div className="model-track" aria-hidden="true">
          <span />
        </div>
        <div className="model-meta">
          <span>{formatTokens(model.total_tokens)} tokens</span>
          <span>{model.requests.toLocaleString('zh-CN')} 次</span>
          <span>${model.cost_usd.toFixed(2)}</span>
        </div>
      </div>
    </article>
  )
}

function MiniMetric({ label, value, detail }: { label: string; value: string; detail: string }) {
  return (
    <article className="mini-card">
      <span>{label}</span>
      <strong>{value}</strong>
      <small>{detail}</small>
    </article>
  )
}

function PanelFooter({
  data,
  connection,
  settings,
}: {
  data: DashboardPayload | null
  connection: ConnectionState
  settings: ConnectionSettings
}) {
  const connected = connection === 'connected'
  const stale = connection === 'stale'
  return (
    <footer className="panel-footer">
      <div className={`connection-dot ${connected ? 'connected' : stale ? 'stale' : ''}`} />
      <div className="footer-status">
        <strong>{connected ? '已连接' : stale ? '连接中断' : '未连接'}</strong>
        <span>{settings.host}:{settings.port}</span>
      </div>
      <div className="footer-meta">
        <span>{data?.version ? `v${data.version}` : '版本未知'}</span>
        {data && <span>{stale ? '上次更新' : '更新于'} {formatTime(data.snapshot.updated_at)}</span>}
      </div>
    </footer>
  )
}

function EmptyState({
  loading,
  error,
  onRetry,
  onSettings,
}: {
  loading: boolean
  error: string
  onRetry: () => void
  onSettings: () => void
}) {
  return (
    <div className="empty-state">
      <div className={`empty-icon ${loading ? 'pulse' : ''}`}>
        {loading ? <RefreshIcon /> : <OfflineIcon />}
      </div>
      <h2>{loading ? '正在连接本机服务' : '无法连接监控服务'}</h2>
      <p>{loading ? '正在读取最新的 Token 用量…' : error || '请确认后端服务正在运行。'}</p>
      {!loading && (
        <div className="empty-actions">
          <button className="primary-button" type="button" onClick={onRetry}>重试</button>
          <button className="secondary-button" type="button" onClick={onSettings}>检查设置</button>
        </div>
      )}
    </div>
  )
}

function ChangeBadge({ current, previous }: { current: number; previous: number }) {
  if (previous <= 0) return <span className="change-badge neutral">—</span>
  const change = ((current - previous) / previous) * 100
  return (
    <span className={`change-badge ${change >= 0 ? 'up' : 'down'}`}>
      {change >= 0 ? '+' : ''}{change.toFixed(1)}%
    </span>
  )
}

function Sparkline({ values }: { values: number[] }) {
  if (values.length < 2) return <div className="sparkline-placeholder" />
  const min = Math.min(...values)
  const max = Math.max(...values)
  const spread = Math.max(max - min, 1)
  const points = values
    .map((value, index) => {
      const x = (index / (values.length - 1)) * 100
      const y = 28 - ((value - min) / spread) * 24
      return `${x},${y}`
    })
    .join(' ')
  return (
    <svg className="sparkline" viewBox="0 0 100 32" preserveAspectRatio="none" aria-hidden="true">
      <polyline points={points} />
    </svg>
  )
}

function LogoMark() {
  return (
    <div className="logo-mark" aria-hidden="true">
      <svg viewBox="0 0 32 32">
        <path d="m16 3 11 6.3v13.4L16 29 5 22.7V9.3L16 3Zm0 3.7-7.7 4.4 7.7 4.5 7.7-4.5L16 6.7ZM8 14.2v6.6l6.3 3.7v-6.7L8 14.2Zm9.7 3.6v6.7l6.3-3.7v-6.6l-6.3 3.6Z" />
      </svg>
    </div>
  )
}

function RefreshIcon() {
  return <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M19 8a7 7 0 1 0 .2 7.6M19 4v4h-4" /></svg>
}

function SettingsIcon() {
  return <svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="3" /><path d="M19.4 15a1.7 1.7 0 0 0 .3 1.9l.1.1-2.8 2.8-.1-.1a1.7 1.7 0 0 0-1.9-.3 1.7 1.7 0 0 0-1 1.6v.2h-4V21a1.7 1.7 0 0 0-1-1.6 1.7 1.7 0 0 0-1.9.3l-.1.1L4.2 17l.1-.1a1.7 1.7 0 0 0 .3-1.9A1.7 1.7 0 0 0 3 14H2.8v-4H3a1.7 1.7 0 0 0 1.6-1 1.7 1.7 0 0 0-.3-1.9L4.2 7 7 4.2l.1.1a1.7 1.7 0 0 0 1.9.3A1.7 1.7 0 0 0 10 3v-.2h4V3a1.7 1.7 0 0 0 1 1.6 1.7 1.7 0 0 0 1.9-.3l.1-.1L19.8 7l-.1.1a1.7 1.7 0 0 0-.3 1.9 1.7 1.7 0 0 0 1.6 1h.2v4H21a1.7 1.7 0 0 0-1.6 1Z" /></svg>
}

function WarningIcon() {
  return <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 3 2.8 20h18.4L12 3Z" /><path d="M12 9v5m0 3h.01" /></svg>
}

function OfflineIcon() {
  return <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M5 12.6A10.8 10.8 0 0 1 19 11m-11 5a6 6 0 0 1 7.7-.5M12 20h.01M3 3l18 18" /></svg>
}

function foldModels(models: PeriodModel[]): PeriodModel[] {
  const sorted = [...models].sort((left, right) => right.total_tokens - left.total_tokens)
  if (sorted.length <= 3) return sorted
  const other = sorted.slice(3).reduce<PeriodModel>(
    (total, model) => ({
      model: '其他模型',
      requests: total.requests + model.requests,
      input_tokens: total.input_tokens + model.input_tokens,
      output_tokens: total.output_tokens + model.output_tokens,
      total_tokens: total.total_tokens + model.total_tokens,
      cost_usd: total.cost_usd + model.cost_usd,
      share: total.share + model.share,
    }),
    {
      model: '其他模型',
      requests: 0,
      input_tokens: 0,
      output_tokens: 0,
      total_tokens: 0,
      cost_usd: 0,
      share: 0,
    },
  )
  return [...sorted.slice(0, 3), other]
}

function formatTokens(value: number): string {
  const absolute = Math.abs(value)
  if (absolute >= 1_000_000) return `${(value / 1_000_000).toFixed(2)}M`
  if (absolute >= 1_000) return `${(value / 1_000).toFixed(1)}K`
  return value.toLocaleString('zh-CN')
}

function comparisonText(current: number, previous: number): string {
  if (previous <= 0) return '暂无上期数据'
  const change = ((current - previous) / previous) * 100
  return `较上期 ${change >= 0 ? '+' : ''}${change.toFixed(1)}%`
}

function formatTime(value: string): string {
  const parsed = new Date(value)
  if (Number.isNaN(parsed.getTime())) return '未知'
  return new Intl.DateTimeFormat('zh-CN', {
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  }).format(parsed)
}

function dashboardScope(
  settings: ConnectionSettings,
  range: UsageRange,
  client: TelemetryClient,
): string {
  return JSON.stringify([settings.host, settings.port, range, client])
}

function errorMessage(reason: unknown): string {
  return reason instanceof Error ? reason.message : String(reason)
}
