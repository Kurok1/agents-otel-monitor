/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 3.0.0
 */

import { useEffect, useState } from 'react'

import type {
  ConnectionSettings,
  MonitorApi,
  ThemeMode,
} from '../monitorApi'
import { SegmentedControl } from './SegmentedControl'

export function SettingsView({
  api,
  settings,
  theme,
  onBack,
  onThemePreview,
  onApply,
}: {
  api: MonitorApi
  settings: ConnectionSettings
  theme: ThemeMode
  onBack: () => void
  onThemePreview: (theme: ThemeMode) => void
  onApply: (settings: ConnectionSettings, theme: ThemeMode, autostart: boolean) => Promise<void>
}) {
  const [host, setHost] = useState(settings.host)
  const [port, setPort] = useState(String(settings.port))
  const [nextTheme, setNextTheme] = useState(theme)
  const [autostart, setAutostart] = useState<boolean | null>(null)
  const [checking, setChecking] = useState(false)
  const [applying, setApplying] = useState(false)
  const [feedback, setFeedback] = useState('')

  useEffect(() => {
    let disposed = false
    void api
      .isAutostartEnabled()
      .then((enabled) => {
        if (!disposed) setAutostart(enabled)
      })
      .catch((reason) => {
        if (!disposed) setFeedback(`无法读取登录项状态 · ${errorMessage(reason)}`)
      })
    return () => {
      disposed = true
    }
  }, [api])

  const draft = (): ConnectionSettings | null => {
    const parsedPort = Number(port)
    if (!host.trim() || !Number.isInteger(parsedPort) || parsedPort < 1 || parsedPort > 65_535) {
      setFeedback('请输入有效的 Host 和 1–65535 端口')
      return null
    }
    return { host: host.trim(), port: parsedPort }
  }

  const testConnection = async () => {
    const candidate = draft()
    if (!candidate) return
    setChecking(true)
    setFeedback('')
    try {
      const result = await api.checkConnection(candidate)
      setFeedback(`连接成功${result.version ? ` · v${result.version}` : ' · 版本未知'}`)
    } catch (reason) {
      setFeedback(`连接失败 · ${errorMessage(reason)}`)
    } finally {
      setChecking(false)
    }
  }

  const apply = async () => {
    const candidate = draft()
    if (!candidate) return
    if (autostart === null) {
      setFeedback('无法应用：登录项状态尚未就绪')
      return
    }
    setApplying(true)
    setFeedback('')
    try {
      await onApply(candidate, nextTheme, autostart)
    } catch (reason) {
      setFeedback(`应用失败 · ${errorMessage(reason)}`)
      try {
        setAutostart(await api.isAutostartEnabled())
      } catch {
        // Keep the requested value when macOS cannot report the actual login-item state.
      }
    } finally {
      setApplying(false)
    }
  }

  return (
    <div className="settings-view">
      <header className="settings-header">
        <button
          className="icon-button"
          type="button"
          aria-label="返回看板"
          disabled={applying}
          onClick={onBack}
        >
          <BackIcon />
        </button>
        <div>
          <p className="eyebrow">VIBECODING MONITOR</p>
          <h1>设置</h1>
        </div>
      </header>
      <div className="settings-scroll">
        <section className="settings-section">
          <div className="settings-title">
            <h2>本机服务</h2>
            <p>设置仅在本次运行期间有效</p>
          </div>
          <label>
            <span>Host</span>
            <input aria-label="Host" value={host} onChange={(event) => setHost(event.target.value)} />
          </label>
          <label>
            <span>Port</span>
            <input
              aria-label="Port"
              inputMode="numeric"
              value={port}
              onChange={(event) => setPort(event.target.value)}
            />
          </label>
          <button className="test-button" type="button" disabled={checking} onClick={() => void testConnection()}>
            {checking ? '正在测试…' : '测试连接'}
          </button>
          {feedback && <p className="settings-feedback" role="status">{feedback}</p>}
        </section>

        <section className="settings-section">
          <div className="settings-title">
            <h2>外观</h2>
            <p>选择后即时预览；点击应用确认，取消恢复原主题</p>
          </div>
          <SegmentedControl
            ariaLabel="主题"
            options={[
              { value: 'system', label: '系统' },
              { value: 'light', label: '浅色' },
              { value: 'dark', label: '深色' },
            ]}
            value={nextTheme}
            onChange={(selectedTheme) => {
              setNextTheme(selectedTheme)
              onThemePreview(selectedTheme)
            }}
          />
        </section>

        <section className="settings-section setting-toggle-row">
          <div className="settings-title">
            <h2>登录时启动</h2>
            <p>由 macOS 登录项管理</p>
          </div>
          <label className="switch">
            <input
              type="checkbox"
              aria-label="登录时启动"
              checked={autostart ?? false}
              disabled={autostart === null || applying}
              onChange={(event) => setAutostart(event.target.checked)}
            />
            <span />
          </label>
        </section>
      </div>
      <footer className="settings-actions">
        <button className="secondary-button" type="button" disabled={applying} onClick={onBack}>取消</button>
        <button
          className="primary-button"
          type="button"
          disabled={applying || autostart === null}
          onClick={() => void apply()}
        >
          {applying ? '正在应用…' : '应用'}
        </button>
      </footer>
    </div>
  )
}

function BackIcon() {
  return <svg viewBox="0 0 24 24" aria-hidden="true"><path d="m15 18-6-6 6-6" /></svg>
}

function errorMessage(reason: unknown): string {
  return reason instanceof Error ? reason.message : String(reason)
}
