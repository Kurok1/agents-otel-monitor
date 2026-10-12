interface Datum {
  name: string;
  value: number;
  color: string;
}

interface Props {
  data: Datum[];
  size?: number;
  centerLabel: string;
  centerValue: string;
}

export function DonutChart({ data, size = 168, centerLabel, centerValue }: Props) {
  const total = data.reduce((s, d) => s + d.value, 0) || 1;
  const r = size / 2;
  const stroke = 22;
  const radius = r - stroke / 2 - 2;
  const C = 2 * Math.PI * radius;

  const arcs = data.reduce<Array<Datum & { len: number; offset: number }>>((segments, d) => {
    const last = segments.at(-1);
    segments.push({ ...d, len: (d.value / total) * C, offset: last ? last.offset + last.len : 0 });
    return segments;
  }, []);
  return (
    <div className="donut-wrap" style={{ width: size, height: size }}>
      <svg viewBox={`0 0 ${size} ${size}`}>
        <circle
          cx={r}
          cy={r}
          r={radius}
          fill="none"
          stroke="var(--bg-alt)"
          strokeWidth={stroke}
        />
        {arcs.map((d, i) => {
          const { len, offset } = d;
          return (
            <circle
              key={i}
              cx={r}
              cy={r}
              r={radius}
              fill="none"
              stroke={d.color}
              strokeWidth={stroke}
              strokeDasharray={`${len} ${C - len}`}
              strokeDashoffset={-offset}
              strokeLinecap="butt"
            />
          );
        })}
      </svg>
      <div className="donut-wrap__center">
        <div className="v">{centerValue}</div>
        <div className="l">{centerLabel}</div>
      </div>
    </div>
  );
}
