/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.0.0
 */

import { useEffect, useState } from 'react';
import { Sessions } from '../api/sessions';
import { costSourceLabel } from '../api/codex-telemetry';
import type { SessionDetail } from '../api/sessions';
import { DonutChart } from '../components/charts/DonutChart';
import { TOOL_PALETTE, SKILL_PALETTE } from '../lib/palette';
import { formatTokens, formatPct, formatCurrency } from '../lib/format';

interface Props {
  id: string;
  client?: 'claude' | 'codex';
  onBack: () => void;
}

function Stat({ label, value, sub }: { label: string; value: string; sub?: string }) {
  return (
    <div className="kpi">
      <div className="kpi__top">
        <div className="kpi__label">
          <span>{label}</span>
        </div>
      </div>
      <div className="kpi__value">
        <span>{value}</span>
      </div>
      {sub && (
        <div className="kpi__foot">
          <span>{sub}</span>
        </div>
      )}
    </div>
  );
}

export function SessionDetailView({ id, client, onBack }: Props) {
  const [d, setD] = useState<SessionDetail | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    Promise.resolve().then(() => {
      if (!cancelled) { setD(null); setErr(null); }
      return Sessions.detail(id, client);
    })
      .then(r => {
        if (!cancelled) setD(r);
      })
      .catch(e => {
        if (!cancelled) setErr(String(e));
      });
    return () => {
      cancelled = true;
    };
  }, [id, client]);

  if (err) {
    return (
      <main className="page">
        <button className="back-btn" onClick={onBack}>← 返回列表</button>
        <section className="card"><div className="card-sub">加载失败：{err}</div></section>
      </main>
    );
  }
  if (!d) {
    return (
      <main className="page">
        <button className="back-btn" onClick={onBack}>← 返回列表</button>
        <section className="card"><div className="card-sub">加载中…</div></section>
      </main>
    );
  }

  const toolsForDonut = d.tools.map((t, i) => ({
    name: t.name,
    value: t.count,
    color: TOOL_PALETTE[i % TOOL_PALETTE.length],
  }));
  const skillsForDonut = d.skills.map((s, i) => ({
    name: s.name,
    value: s.activations,
    color: SKILL_PALETTE[i % SKILL_PALETTE.length],
  }));

  const started = new Date(d.first_active).toLocaleString('zh-CN');
  const ended = new Date(d.last_active).toLocaleString('zh-CN');

  return (
    <main className="page">
      <button className="back-btn" onClick={onBack}>← 返回列表</button>

      <div className="page-hero">
        <div>
          <h1>会话详情</h1>
          <p>
            <span className={`client-badge client-badge--${d.client}`}>
              {d.client === 'codex' ? 'Codex' : 'Claude'}
            </span>
            <span className="session-id">{d.session_id}</span>
            <br />
            {started} → {ended}
          </p>
        </div>
      </div>

      <div className="kpi-grid">
        <Stat
          label="Token 用量"
          value={formatTokens(d.tokens)}
          sub={
            d.token_detail
              ? `输入 ${formatTokens(d.token_detail.input)} · 输出 ${formatTokens(d.token_detail.output)} · 缓存 ${formatTokens(d.token_detail.cached)} · 缓存写入 ${formatTokens(d.token_detail.cache_write ?? 0)} · 推理 ${formatTokens(d.token_detail.reasoning)}`
              : undefined
          }
        />
        <Stat label="请求次数" value={d.requests.toLocaleString()} />
        <Stat label="工具调用" value={d.tool_calls.toLocaleString()} />
        {d.cost != null && (
          <Stat label={`成本${d.cost_estimated ? '(含估算)' : ''}`} value={formatCurrency(d.cost)} sub={d.client === 'codex' ? costSourceLabel(d) : undefined} />
        )}
        {(
          <Stat label={d.client === 'codex' ? 'Skill 调用（已观测）' : 'Skill 激活'} value={d.skill_activations.toLocaleString()} />
        )}
      </div>

      {d.client === 'codex' && d.cost == null && <p className="card-sub">成本未知：尚未收到可用的原生估值或本地估价。</p>}
      {d.client === 'codex' && d.local_estimate_usd != null && <p className="card-sub">本地估价参考：{formatCurrency(d.local_estimate_usd)}，不与原生估值相加。</p>}
      <div className="cols-2">
        <section className="card">
          <div className="card-head">
            <div>
              <h3>工具调用次数</h3>
              <div className="card-sub">{d.tool_calls.toLocaleString()} 次累计</div>
            </div>
          </div>
          {d.tools.length === 0 ? (
            <div className="card-sub">本会话无工具调用</div>
          ) : (
            <div className="rank">
              <DonutChart
                data={toolsForDonut}
                centerLabel="次工具调用"
                centerValue={formatTokens(d.tool_calls).replace('.0', '')}
              />
              <div className="rank-list">
                {d.tools.map((t, i) => (
                  <div className="rank-list__row" key={t.name}>
                    <span className="rank-list__dot" style={{ background: TOOL_PALETTE[i % TOOL_PALETTE.length] }} />
                    <span className="rank-list__name">{t.name}</span>
                    <span className="rank-list__count">{t.count.toLocaleString()}</span>
                    <span className="rank-list__pct">
                      {formatPct(t.count / (d.tool_calls || 1), 1)}
                    </span>
                  </div>
                ))}
              </div>
            </div>
          )}
        </section>

        {(
        <section className="card">
          <div className="card-head">
            <div>
              <h3>{d.client === 'codex' ? 'Skill 调用次数' : 'Skill 激活次数'}</h3>
              <div className="card-sub">{d.skill_activations.toLocaleString()} 次累计{d.client === 'codex' && ' · 已按 Turn 去重，调用不代表任务成功'}</div>
            </div>
          </div>
          {d.skills.length === 0 ? (
            <div className="card-sub">{d.client === 'codex' ? '尚未收到本会话的 Skill 调用遥测' : '本会话无 Skill 激活'}</div>
          ) : (
            <div className="rank">
              <DonutChart
                data={skillsForDonut}
                centerLabel={d.client === 'codex' ? '次调用' : '次激活'}
                centerValue={d.skill_activations.toLocaleString()}
              />
              <div className="rank-list">
                {d.skills.map((s, i) => (
                  <div className="rank-list__row" key={s.name}>
                    <span
                      className="rank-list__dot"
                      style={{ background: SKILL_PALETTE[i % SKILL_PALETTE.length] }}
                    />
                    <span className="rank-list__name">{s.name}{d.client === 'codex' && <small> · 显式 {s.explicit ?? 0} / 隐式 {s.implicit ?? 0}</small>}</span>
                    <span className="rank-list__count">{s.activations.toLocaleString()}</span>
                    <span className="rank-list__pct">
                      {formatPct(s.activations / (d.skill_activations || 1), 1)}
                    </span>
                  </div>
                ))}
              </div>
            </div>
          )}
        </section>
        )}
      </div>
    </main>
  );
}
