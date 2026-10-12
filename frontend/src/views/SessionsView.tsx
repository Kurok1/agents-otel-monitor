/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.0.0
 */

import { useEffect, useState } from 'react';
import { Sessions } from '../api/sessions';
import { costSourceLabel } from '../api/codex-telemetry';
import type { SessionSummary } from '../api/sessions';
import { formatTokens, formatCurrency } from '../lib/format';

function fmtTime(iso: string): string {
  return new Date(iso).toLocaleString('zh-CN', {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  });
}

interface Props {
  client: 'all' | 'claude' | 'codex';
  onOpen: (id: string, client: 'claude' | 'codex') => void;
}

export function SessionsView({ client, onOpen }: Props) {
  const [rows, setRows] = useState<SessionSummary[] | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    Promise.resolve().then(() => {
      if (!cancelled) setRows(null);
      return Sessions.list(50, client);
    })
      .then(r => {
        if (!cancelled) setRows(r.sessions);
      })
      .catch(e => {
        if (!cancelled) setErr(String(e));
      });
    return () => {
      cancelled = true;
    };
  }, [client]);

  return (
    <main className="page">
      <div className="section-head">
        <div>
          <h2>会话列表</h2>
          <p>按最近活动时间排序 · 点击查看详情</p>
        </div>
      </div>

      <section className="card">
        {err && <div className="card-sub">加载失败：{err}</div>}
        {!rows && !err && <div className="card-sub">加载中…</div>}
        {rows && rows.length === 0 && <div className="card-sub">暂无会话数据</div>}
        {rows && rows.length > 0 && (
          <div className="model-table-scroll"><table className="model-table session-table">
            <thead>
              <tr>
                <th>会话 ID</th>
                <th className="num">请求</th>
                <th className="num">Tokens</th>
                <th className="num">工具</th>
                <th className="num">Skill</th>
                <th className="num">费用</th>
                <th className="num">最近活动</th>
              </tr>
            </thead>
            <tbody>
              {rows.map(s => (
                <tr
                  key={`${s.client}:${s.session_id}`}
                  className="session-row"
                  onClick={() => onOpen(s.session_id, s.client)}
                >
                  <td>
                    <span className={`client-badge client-badge--${s.client}`}>
                      {s.client === 'codex' ? 'Codex' : 'Claude'}
                    </span>
                    <span className="session-id">{s.session_id}</span>
                  </td>
                  <td className="num">{s.requests.toLocaleString()}</td>
                  <td className="num">{formatTokens(s.tokens)}</td>
                  <td className="num">{s.tool_calls.toLocaleString()}</td>
                  <td className="num">{s.skill_activations.toLocaleString()}</td>
                  <td className="num">
                    {s.cost != null ? formatCurrency(s.cost) : '—'}
                    {s.cost_estimated && s.cost != null && <span className="est-tag" title={costSourceLabel(s)}> 估 · {costSourceLabel(s).split(' · ')[0]}</span>}
                  </td>
                  <td className="num">{fmtTime(s.last_active)}</td>
                </tr>
              ))}
            </tbody>
          </table></div>
        )}
      </section>
    </main>
  );
}
