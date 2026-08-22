import { useId, useRef, useState } from "react";

/**
 * One measurement over time.
 *
 * Inline SVG and no charting library. The dashboard has a bundle-size gate and
 * no UI framework by choice, and a line against time is arithmetic — importing
 * three hundred kilobytes to draw a polyline would be the largest dependency in
 * the product for its simplest picture.
 *
 * Deliberately **one measurement per chart**, never two scales on one plot. Two
 * y-axes invent a correlation that is not in the data: the alignment between
 * them is arbitrary, so any crossing or divergence is an artefact of where the
 * scales were put. Where two things genuinely belong together — bytes down and
 * bytes up — they share one axis because they share one unit.
 */

/**
 * A round number near a tenth of the range: 1, 2, 5 and their powers of ten.
 *
 * The set every hand-drawn axis has used for a century, and for the reason it
 * has: those are the numbers people divide by without thinking.
 */
function niceStep(spread: number): number {
  const magnitude = 10 ** Math.floor(Math.log10(Math.max(spread, 1e-9) / 4));
  for (const multiple of [1, 2, 5]) {
    if (magnitude * multiple * 4 >= spread) return magnitude * multiple;
  }
  return magnitude * 10;
}

export interface Series {
  label: string;
  /** null where nothing was recorded. Never zero: absent and idle are different. */
  values: (number | null)[];
  /** 1 or 2 — the validated slot, not a free colour. */
  slot: 1 | 2;
}

export function TimeChart({
  title,
  unit,
  times,
  series,
  format,
  axisFormat,
  /** Fixed upper bound, for a measurement whose scale is meaningful on its own —
   *  a percentage is 0–100 whether or not the machine ever reached it, and a
   *  chart that rescales to its own maximum makes 4% look like a crisis. */
  max,
  /** Anchor the bottom at zero. True where zero means something — no traffic is
   *  a real reading. False where it does not: a room-temperature server drawn
   *  from 0 °C spends nine tenths of the plot proving it is not frozen, and the
   *  forty degrees that matter are squeezed into the top. */
  zero = false,
}: {
  title: string;
  unit: string;
  times: string[];
  series: Series[];
  format: (value: number) => string;
  /** Short form for the two axis ticks. Defaults to a rounded number, which is
   *  right for a percentage and wrong for bytes — ten million of them is
   *  "9.8 MB", not "10279k". */
  axisFormat?: (value: number) => string;
  max?: number;
  zero?: boolean;
}) {
  const [hover, setHover] = useState<number | null>(null);
  const svg = useRef<SVGSVGElement>(null);
  const clipId = useId();

  const drawable = series.filter((one) => one.values.some((value) => value !== null));
  if (drawable.length === 0 || times.length < 2) {
    return (
      <figure className="chart">
        <figcaption>{title}</figcaption>
        <p className="muted chart-empty">Not enough recorded yet.</p>
      </figure>
    );
  }

  const width = 720;
  const height = 150;
  // Room for the widest tick this can produce. The unit lives in the caption
  // rather than on every tick, which is what keeps them short enough to fit —
  // an axis label that runs off the left edge is worse than no axis at all.
  const pad = { left: 52, right: 12, top: 10, bottom: 18 };
  const plotW = width - pad.left - pad.right;
  const plotH = height - pad.top - pad.bottom;

  const present = drawable.flatMap((one) =>
    one.values.filter((value): value is number => value !== null),
  );
  const seen = { low: Math.min(...present), high: Math.max(...present) };
  // A flat series has no range to scale against, and dividing by it draws
  // nothing. Given room on both sides it becomes the flat line it is, through
  // the middle — along the floor would read as "it stopped".
  const spread = seen.high - seen.low < 1 ? 1 : seen.high - seen.low;
  // Rounded outward to a readable step. An axis reading 2316 to 4780 is the
  // data's own extremes printed as if they were meaningful — they are an
  // artefact of when the sampling happened to start, and they read as noise
  // where 2000 and 5000 read as a scale.
  const step = niceStep(spread);
  const low =
    max !== undefined || zero ? 0 : Math.floor((seen.low - spread * 0.1) / step) * step;
  const top = max ?? Math.ceil((seen.high + spread * 0.1) / step) * step;

  const x = (i: number) => pad.left + (i / (times.length - 1)) * plotW;
  const y = (value: number) =>
    pad.top + plotH - ((value - low) / (top - low || 1)) * plotH;

  // Gaps are gaps. A missing reading joined by a straight line across it claims
  // a measurement nobody took — which on a machine that was switched off for a
  // day is a confident line through nothing.
  const paths = (values: (number | null)[]) => {
    const out: string[] = [];
    let run: string[] = [];
    values.forEach((value, i) => {
      if (value === null) {
        if (run.length > 1) out.push(run.join(" "));
        run = [];
        return;
      }
      run.push(`${run.length === 0 ? "M" : "L"}${x(i)} ${y(value)}`);
    });
    if (run.length > 1) out.push(run.join(" "));
    return out;
  };

  const track = (event: React.PointerEvent<SVGSVGElement>) => {
    const box = svg.current?.getBoundingClientRect();
    if (!box) return;
    const at = ((event.clientX - box.left) / box.width) * width;
    const index = Math.round(((at - pad.left) / plotW) * (times.length - 1));
    setHover(Math.min(times.length - 1, Math.max(0, index)));
  };

  // Bare numbers on the axis; the unit is in the caption. The readout under the
  // chart carries the full formatted value, where there is room for it.
  const tick =
    axisFormat ??
    ((value: number) =>
      Math.abs(value) >= 10000
        ? `${Math.round(value / 1000)}k`
        : `${Math.round(value)}`);

  const when = (stamp: string) =>
    new Date(stamp).toLocaleString(undefined, {
      day: "numeric",
      month: "short",
      hour: "2-digit",
      minute: "2-digit",
    });

  return (
    <figure className="chart">
      <figcaption>
        {title} <span className="chart-unit">{unit}</span>
        {/* A legend only where there is more than one series. With one, the
            title names it and a legend box repeats itself. */}
        {drawable.length > 1 ? (
          <span className="legend">
            {drawable.map((one) => (
              <span key={one.label} className={`key key-${one.slot}`}>
                {one.label}
              </span>
            ))}
          </span>
        ) : null}
      </figcaption>

      <svg
        ref={svg}
        viewBox={`0 0 ${width} ${height}`}
        role="img"
        aria-label={`${title}, ${when(times[0] ?? "")} to ${when(times[times.length - 1] ?? "")}`}
        onPointerMove={track}
        onPointerLeave={() => setHover(null)}
      >
        <defs>
          <clipPath id={clipId}>
            <rect x={pad.left} y={pad.top} width={plotW} height={plotH} />
          </clipPath>
        </defs>

        {/* Recessive: a hairline at top and bottom, nothing between. A grid
            that competes with the data is a grid being read instead of it. */}
        <line
          className="chart-grid-line"
          x1={pad.left}
          x2={width - pad.right}
          y1={pad.top}
          y2={pad.top}
        />
        <line
          className="chart-axis-line"
          x1={pad.left}
          x2={width - pad.right}
          y1={pad.top + plotH}
          y2={pad.top + plotH}
        />

        <g clipPath={`url(#${clipId})`}>
          {drawable.map((one) =>
            paths(one.values).map((d, i) => (
              <path key={`${one.label}-${i}`} className={`line line-${one.slot}`} d={d} />
            )),
          )}
        </g>

        {hover !== null ? (
          <>
            <line
              className="chart-crosshair"
              x1={x(hover)}
              x2={x(hover)}
              y1={pad.top}
              y2={pad.top + plotH}
            />
            {drawable.map((one) => {
              const value = one.values[hover];
              return value === null || value === undefined ? null : (
                <circle
                  key={one.label}
                  className={`dot dot-${one.slot}`}
                  cx={x(hover)}
                  cy={y(value)}
                  r={4}
                />
              );
            })}
          </>
        ) : null}

        {/* Two ticks, top and bottom. A tick against every gridline is a
            number being read instead of the shape it is drawn on. */}
        <text className="chart-tick" x={pad.left - 6} y={pad.top + 4} textAnchor="end">
          {tick(top)}
        </text>
        <text className="chart-tick" x={pad.left - 6} y={pad.top + plotH} textAnchor="end">
          {tick(low)}
        </text>
      </svg>

      {/* The reading under the chart rather than floating over it. A tooltip
          that follows the pointer covers the line it is describing, and on a
          touch screen there is no pointer to follow. */}
      <div className="chart-readout" aria-live="polite">
        {hover === null ? (
          <span className="muted">
            {when(times[0] ?? "")} — {when(times[times.length - 1] ?? "")}
          </span>
        ) : (
          <>
            <span className="muted">{when(times[hover] ?? "")}</span>
            {drawable.map((one) => {
              const value = one.values[hover];
              return (
                <span key={one.label} className={`key key-${one.slot}`}>
                  {one.label} {value === null || value === undefined ? "—" : format(value)}
                </span>
              );
            })}
          </>
        )}
      </div>
    </figure>
  );
}
