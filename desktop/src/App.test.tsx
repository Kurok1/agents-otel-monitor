/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 3.0.0
 */

import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import App from './App'
import type { DashboardPayload, MonitorApi, PanelEvent } from './monitorApi'

afterEach(() => {
  vi.useRealTimers()
  cleanup()
})

const dashboard: DashboardPayload = {
  snapshot: {
    updated_at: '2026-08-12T10:42:00Z',
    range: 'day',
    tokens: {
      in: 9_000_000,
      out: 3_840_000,
      total: 12_840_000,
      prev_total: 10_000_000,
      sparkline: [7_000_000, 9_000_000, 8_400_000, 12_840_000],
    },
    cost: {
      total: 32.48,
      prev_total: 28.1,
      sparkline: [19.2, 24.1, 27.8, 32.48],
      cost_estimated: true,
    },
    cache: {
      hit_rate: 0.75,
      read_tokens: 3_000_000,
      creation_tokens: 1_000_000,
    },
    requests: {
      total: 42,
      prev_total: 36,
      sparkline: [25, 31, 39, 42],
    },
  },
  models: {
    updated_at: '2026-08-12T10:42:00Z',
    range: 'day',
    client: 'all',
    cost_estimated: true,
    models: [
      model('Claude Opus 4.8', 5_620_000, 0.438, 18.43),
      model('Claude Sonnet 4.6', 3_420_000, 0.266, 7.82),
      model('GPT-5.6', 1_860_000, 0.145, 4.13),
      model('DeepSeek V3', 1_160_000, 0.09, 1.42),
      model('Gemini 3 Pro', 780_000, 0.061, 0.68),
    ],
  },
  version: '3.0',
}

describe('Vibecoding Monitor panel', () => {
  it('renders current usage and folds model rows after the top three', async () => {
    render(<App api={createApi()} />)

    expect(await screen.findByText('12.84M')).toBeVisible()
    expect(screen.getByText('$32.48')).toBeVisible()
    expect(screen.getByText('Claude Opus 4.8')).toBeVisible()
    expect(screen.getByText('Claude Sonnet 4.6')).toBeVisible()
    expect(screen.getByText('GPT-5.6')).toBeVisible()
    expect(screen.getByText('其他模型')).toBeVisible()
    expect(screen.queryByText('DeepSeek V3')).not.toBeInTheDocument()
    expect(screen.getByText('v3.0')).toBeVisible()
    expect(screen.getByText('已连接')).toBeVisible()
  })

  it('keeps the last successful data visible when a refresh fails', async () => {
    let refreshes = 0
    const api = createApi({
      fetchDashboard: async () => {
        refreshes += 1
        if (refreshes === 1) return dashboard
        throw new Error('server unavailable')
      },
    })
    render(<App api={api} />)

    expect(await screen.findByText('12.84M')).toBeVisible()
    fireEvent.click(screen.getByRole('button', { name: '立即刷新' }))

    expect(await screen.findByText('连接中断')).toBeVisible()
    expect(screen.getByText('12.84M')).toBeVisible()
    expect(screen.getByText(/上次更新/)).toBeVisible()
  })

  it('shows a recoverable offline state when startup cannot reach the server', async () => {
    const api = createApi({
      fetchDashboard: async () => {
        throw new Error('connection refused')
      },
    })
    render(<App api={api} />)

    expect(await screen.findByText('无法连接监控服务')).toBeVisible()
    expect(screen.getByText('connection refused')).toBeVisible()
    expect(screen.getByRole('button', { name: '重试' })).toBeVisible()
    expect(screen.getByRole('button', { name: '检查设置' })).toBeVisible()
  })

  it('explains when an older service does not provide period model data', async () => {
    const api = createApi({
      fetchDashboard: async () => ({
        ...dashboard,
        models: { ...dashboard.models, available: false, models: [] },
      }),
    })
    render(<App api={api} />)

    expect(
      await screen.findByText('当前服务版本不支持周期模型统计，请重启升级后的后端'),
    ).toBeVisible()
    expect(screen.getByText('12.84M')).toBeVisible()
    expect(screen.getByText('已连接')).toBeVisible()
  })

  it('refreshes immediately after switching client and period', async () => {
    const api = createApi({
      fetchDashboard: async (_settings, range, client) => ({
        ...dashboard,
        snapshot: {
          ...dashboard.snapshot,
          range,
          tokens: {
            ...dashboard.snapshot.tokens,
            total: range === 'week' && client === 'codex' ? 21_000_000 : 12_840_000,
          },
        },
        models: { ...dashboard.models, range, client },
      }),
    })
    render(<App api={api} />)

    expect(await screen.findByText('12.84M')).toBeVisible()
    fireEvent.click(screen.getByRole('button', { name: 'Codex' }))
    fireEvent.click(screen.getByRole('button', { name: '本周' }))

    expect(await screen.findByText('21.00M')).toBeVisible()
    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Codex' })).toHaveAttribute(
        'aria-pressed',
        'true',
      )
      expect(screen.getByRole('button', { name: '本周' })).toHaveAttribute(
        'aria-pressed',
        'true',
      )
    })
  })

  it('applies session settings even when the connection test fails', async () => {
    const api = createApi({
      checkConnection: async () => {
        throw new Error('connection refused')
      },
    })
    render(<App api={api} />)

    expect(await screen.findByText('12.84M')).toBeVisible()
    fireEvent.click(screen.getByRole('button', { name: '打开设置' }))
    fireEvent.change(screen.getByRole('textbox', { name: 'Host' }), {
      target: { value: 'localhost' },
    })
    fireEvent.change(screen.getByRole('textbox', { name: 'Port' }), {
      target: { value: '9200' },
    })
    fireEvent.click(screen.getByRole('button', { name: '深色' }))
    fireEvent.click(screen.getByRole('button', { name: '测试连接' }))

    expect(await screen.findByText(/连接失败/)).toBeVisible()
    fireEvent.click(screen.getByRole('button', { name: '应用' }))

    expect(await screen.findByText('localhost:9200')).toBeVisible()
    expect(document.documentElement).toHaveAttribute('data-theme', 'dark')
  })

  it('keeps settings open and reports an autostart update failure', async () => {
    const api = createApi({
      setAutostartEnabled: async () => {
        throw new Error('login item denied')
      },
    })
    render(<App api={api} />)

    expect(await screen.findByText('12.84M')).toBeVisible()
    fireEvent.click(screen.getByRole('button', { name: '打开设置' }))
    const autostart = screen.getByRole('checkbox', { name: '登录时启动' })
    await waitFor(() => expect(autostart).toBeEnabled())
    fireEvent.click(autostart)
    fireEvent.click(screen.getByRole('button', { name: '应用' }))

    expect(await screen.findByText(/应用失败.*login item denied/)).toBeVisible()
    expect(screen.getByRole('heading', { name: '设置' })).toBeVisible()
    expect(screen.getByRole('checkbox', { name: '登录时启动' })).not.toBeChecked()
  })

  it('does not overwrite autostart when its current state cannot be read', async () => {
    const setAutostartEnabled = vi.fn(async () => undefined)
    const api = createApi({
      isAutostartEnabled: async () => {
        throw new Error('login item unavailable')
      },
      setAutostartEnabled,
    })
    render(<App api={api} />)

    expect(await screen.findByText('12.84M')).toBeVisible()
    fireEvent.click(screen.getByRole('button', { name: '打开设置' }))

    expect(await screen.findByText(/无法读取登录项状态.*login item unavailable/)).toBeVisible()
    expect(screen.getByRole('checkbox', { name: '登录时启动' })).toBeDisabled()
    expect(screen.getByRole('button', { name: '应用' })).toBeDisabled()
    expect(setAutostartEnabled).not.toHaveBeenCalled()
  })

  it('stops polling while hidden and refreshes as soon as the panel reopens', async () => {
    let refreshes = 0
    const reopened = {
      ...dashboard,
      snapshot: {
        ...dashboard.snapshot,
        tokens: { ...dashboard.snapshot.tokens, total: 19_500_000 },
      },
    }
    const { api, emit } = createEventApi({
      fetchDashboard: async () => {
        refreshes += 1
        return refreshes === 1 ? dashboard : reopened
      },
    })
    render(<App api={api} />)
    expect(await screen.findByText('12.84M')).toBeVisible()

    act(() => emit('panel-hidden'))
    vi.useFakeTimers()
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5 * 60 * 1_000)
    })
    expect(screen.queryByText('19.50M')).not.toBeInTheDocument()

    act(() => emit('panel-shown'))
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1)
    })
    expect(screen.getByText('19.50M')).toBeVisible()
  })
})

function model(modelName: string, totalTokens: number, share: number, cost: number) {
  return {
    model: modelName,
    requests: 8,
    input_tokens: Math.round(totalTokens * 0.7),
    output_tokens: Math.round(totalTokens * 0.3),
    total_tokens: totalTokens,
    cost_usd: cost,
    share,
  }
}

function createApi(overrides: Partial<MonitorApi> = {}): MonitorApi {
  return {
    fetchDashboard: async () => dashboard,
    checkConnection: async () => ({ version: '3.0' }),
    hidePanel: async () => undefined,
    isPanelVisible: async () => true,
    isAutostartEnabled: async () => false,
    setAutostartEnabled: async () => undefined,
    listen: async () => () => undefined,
    ...overrides,
  }
}

function createEventApi(overrides: Partial<MonitorApi> = {}) {
  const listeners = new Map<PanelEvent, Set<() => void>>()
  const api = createApi({
    listen: async (event, handler) => {
      const handlers = listeners.get(event) ?? new Set<() => void>()
      handlers.add(handler)
      listeners.set(event, handlers)
      return () => handlers.delete(handler)
    },
    ...overrides,
  })
  return {
    api,
    emit: (event: PanelEvent) => listeners.get(event)?.forEach((handler) => handler()),
  }
}
