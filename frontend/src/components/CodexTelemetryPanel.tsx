/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 3.2.0
 */

import type { CodexTelemetry } from '../api/codex-telemetry';
import { formatTokens } from '../lib/format';

const tokenLabels: Record<string, string> = {
  input: '输入', output: '输出', cached_input: '缓存读取',
  cache_write_input: '缓存写入', reasoning_output: '推理输出', total: '总量',
};
const milliseconds = (value: number | null) => value == null ? '未知' : `${value.toLocaleString('zh-CN', { maximumFractionDigits: 1 })} ms`;
const tokens = (value: number | null) => value == null ? '未知' : formatTokens(value);

export function CodexTelemetryPanel({ data }: { data: CodexTelemetry | null }) {
  return <section className="card codex-telemetry">
    <div className="card-head"><div>
      <h3>Codex 延迟与用量核对</h3>
      <div className="card-sub">响应流首输出延迟（TTFT），含推理输出，不含建立连接和接收响应头的时间</div>
    </div></div>
    {data == null ? <p className="card-sub">遥测详情暂不可用，请刷新重试。</p> : <>
      <div className="telemetry-stats">
        <div><span>样本</span><strong>{data.ttft.samples.toLocaleString()}</strong></div>
        <div><span>平均 TTFT</span><strong>{milliseconds(data.ttft.mean_ms)}</strong></div>
        <div><span>P50</span><strong>{milliseconds(data.ttft.p50_ms)}</strong></div>
        <div><span>P95</span><strong>{milliseconds(data.ttft.p95_ms)}</strong></div>
      </div>
      <p className="card-sub">差值 = Turn 用量 − 响应用量。跨时间窗口、采样缺失或丢失遥测可能造成差异；核对结果不修改主统计。缓存与推理属于子集，不与总量相加。</p>
      {data.models.length === 0 ? <p className="card-sub">当前时间范围尚无 Codex 遥测。</p> :
        <div className="telemetry-table-scroll"><table className="model-table">
          <thead><tr><th>模型 / Token 类型</th><th className="num">响应 Token</th><th className="num">Turn Token</th><th className="num">差值</th><th className="num">响应 / Turn 样本</th></tr></thead>
          <tbody>{data.models.flatMap(model => model.usage.map(usage =>
            <tr key={`${model.model}:${usage.token_type}`}>
              <td>{model.model || '未知模型'} · {tokenLabels[usage.token_type] ?? usage.token_type}</td>
              <td className="num">{tokens(usage.response_tokens)}</td>
              <td className="num">{tokens(usage.turn_tokens)}</td>
              <td className="num">{tokens(usage.difference)}</td>
              <td className="num">{usage.response_samples.toLocaleString()} / {usage.turn_samples.toLocaleString()}</td>
            </tr>))}</tbody>
        </table></div>}
    </>}
  </section>;
}
