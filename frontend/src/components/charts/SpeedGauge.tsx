/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.5.1
 */

interface SpeedGaugeProps {
  value: number | null;
  previous: number | null;
  windowSeconds: number;
  asOf: string;
  sourceLabel: string;
  metricLabel: string;
  color: string;
}

function clamp(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value));
}

function scaleMax(value: number | null): number {
  if (value == null || value <= 100) return 100;
  return Math.ceil(value / 25) * 25;
}

function displaySpeed(value: number | null): string {
  return value == null ? '—' : value.toFixed(1);
}

function displayWindow(seconds: number): string {
  if (seconds > 0 && seconds % 60 === 0) return `${seconds / 60} 分钟`;
  return `${seconds} 秒`;
}

function displayUpdatedAt(value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '更新时间未知';
  return `更新于 ${date.toLocaleTimeString('zh-CN', {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  })}`;
}

export function SpeedGauge({
  value,
  previous,
  windowSeconds,
  asOf,
  sourceLabel,
  metricLabel,
  color,
}: SpeedGaugeProps) {
  const max = scaleMax(value);
  const progress = value == null ? 0 : clamp(value / max, 0, 1);
  const needleRotation = -90 + progress * 180;
  const delta =
    value != null && previous != null && previous > 0
      ? ((value - previous) / previous) * 100
      : null;
  const windowLabel = displayWindow(windowSeconds);
  const accessibleValue = value == null ? '暂无数据' : `${displaySpeed(value)} tok/s`;
  const ticks = Array.from({ length: 11 }, (_, index) => {
    const angle = Math.PI - (index / 10) * Math.PI;
    const inner = 113;
    const outer = index % 5 === 0 ? 128 : 122;
    return {
      x1: 160 + inner * Math.cos(angle),
      y1: 155 - inner * Math.sin(angle),
      x2: 160 + outer * Math.cos(angle),
      y2: 155 - outer * Math.sin(angle),
    };
  });

  return (
    <section className="card speed-gauge-card">
      <div className="speed-gauge__head">
        <div>
          <h3>即时生成速度</h3>
          <div className="card-sub">最近 {windowLabel} · {metricLabel}</div>
        </div>
        {delta != null && (
          <span className={`speed-gauge__delta ${delta >= 0 ? 'up' : 'down'}`}>
            {delta >= 0 ? '↑' : '↓'} {Math.abs(delta).toFixed(1)}%
          </span>
        )}
      </div>

      <div className="speed-gauge__dial">
        <svg
          viewBox="0 0 320 190"
          role="img"
          aria-label={`即时生成速度 ${accessibleValue}，最近 ${windowLabel}平均`}
        >
          <path
            className="speed-gauge__track"
            d="M 32 155 A 128 128 0 0 1 288 155"
            pathLength="100"
          />
          <path
            className="speed-gauge__value"
            d="M 32 155 A 128 128 0 0 1 288 155"
            pathLength="100"
            stroke={color}
            strokeDasharray={`${progress * 100} 100`}
          />
          <g className="speed-gauge__ticks">
            {ticks.map((tick, index) => (
              <line key={index} {...tick} />
            ))}
          </g>
          <line
            className="speed-gauge__needle"
            x1="160"
            y1="155"
            x2="160"
            y2="54"
            transform={`rotate(${needleRotation} 160 155)`}
          />
          <circle className="speed-gauge__hub" cx="160" cy="155" r="9" />
          <text className="speed-gauge__scale" x="29" y="178">0</text>
          <text className="speed-gauge__scale" x="160" y="24" textAnchor="middle">{max / 2}</text>
          <text className="speed-gauge__scale" x="291" y="178" textAnchor="end">{max}</text>
        </svg>

        <div className="speed-gauge__readout">
          <strong>{displaySpeed(value)}</strong>
          <span>tok/s</span>
        </div>
      </div>

      <div className="speed-gauge__footer">
        <span className="speed-gauge__source">
          <i style={{ background: color }} />
          {sourceLabel}
        </span>
        <span>{displayUpdatedAt(asOf)}</span>
      </div>
    </section>
  );
}
