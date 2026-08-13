/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 3.0.0
 */

import { invoke } from '@tauri-apps/api/core'
import { listen as listenToTauriEvent } from '@tauri-apps/api/event'
import { getCurrentWindow } from '@tauri-apps/api/window'
import {
  disable as disableAutostart,
  enable as enableAutostart,
  isEnabled as isAutostartEnabled,
} from '@tauri-apps/plugin-autostart'

export type UsageRange = 'day' | 'week' | 'month'
export type TelemetryClient = 'all' | 'claude' | 'codex'
export type ThemeMode = 'system' | 'light' | 'dark'
export type PanelEvent =
  | 'panel-shown'
  | 'panel-hidden'
  | 'refresh-requested'
  | 'settings-requested'

export interface ConnectionSettings {
  host: string
  port: number
}

export interface DashboardPayload {
  snapshot: SnapshotResponse
  models: PeriodModelsResponse
  version: string | null
}

export interface SnapshotResponse {
  updated_at: string
  range: UsageRange
  tokens: {
    in: number
    out: number
    total: number
    prev_total: number
    sparkline: number[]
  }
  cost: {
    total: number
    prev_total: number
    sparkline: number[]
    cost_estimated: boolean
  }
  cache: {
    hit_rate: number | null
    read_tokens: number
    creation_tokens: number
  }
  requests: {
    total: number
    prev_total: number
    sparkline: number[]
  }
}

export interface PeriodModelsResponse {
  updated_at: string
  range: UsageRange
  client: TelemetryClient
  cost_estimated: boolean
  models: PeriodModel[]
  available?: boolean
}

export interface PeriodModel {
  model: string
  requests: number
  input_tokens: number
  output_tokens: number
  total_tokens: number
  cost_usd: number
  share: number
}

export interface ConnectionCheck {
  version: string | null
}

export interface MonitorApi {
  fetchDashboard(
    settings: ConnectionSettings,
    range: UsageRange,
    client: TelemetryClient,
  ): Promise<DashboardPayload>
  checkConnection(settings: ConnectionSettings): Promise<ConnectionCheck>
  hidePanel(): Promise<void>
  isPanelVisible(): Promise<boolean>
  isAutostartEnabled(): Promise<boolean>
  setAutostartEnabled(enabled: boolean): Promise<void>
  listen(event: PanelEvent, handler: () => void): Promise<() => void>
}

export const defaultConnectionSettings: ConnectionSettings = {
  host: '127.0.0.1',
  port: 9_100,
}

export const tauriMonitorApi: MonitorApi = {
  fetchDashboard: (settings, range, client) =>
    invoke<DashboardPayload>('fetch_dashboard', { settings, range, client }),
  checkConnection: (settings) =>
    invoke<ConnectionCheck>('check_connection', { settings }),
  hidePanel: () => invoke<void>('hide_panel'),
  isPanelVisible: () => getCurrentWindow().isVisible(),
  isAutostartEnabled,
  setAutostartEnabled: async (enabled) => {
    if (enabled) {
      await enableAutostart()
    } else {
      await disableAutostart()
    }
  },
  listen: async (event, handler) => {
    const unlisten = await listenToTauriEvent(event, handler)
    return unlisten
  },
}
