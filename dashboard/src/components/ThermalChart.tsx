import type { ThermalSample } from "../api";

/**
 * How hot the machine has been, drawn as a line.
 *
 * An inline SVG and no charting library. The dashboard has a bundle-size gate
 * and no UI framework by choice, and a chart of one series against time is
 * about forty lines of arithmetic — importing three hundred kilobytes to draw
 * a polyline would be the largest dependency in the product, for its simplest
 * picture.
 *
 * The shape is the whole point. Every question worth asking here is a
 * comparison — hotter than last week, climbs whenever something transcodes,
 * did cleaning the fan help — and none of them are answerable from the single
 * number on the overview.
 */
export function ThermalChart({
  samples,
  field,
  unit,
  label,
}: {
  samples: ThermalSample[];
  field: "celsius" | "fan_rpm";
  unit: string;
  label: string;
}) {
  const points = samples
    .map((sample) => ({ time: sample.time, value: sample[field] }))
    .filter((point): point is { time: string; value: number } => point.value !== null);

  // A handful of points is a picture of nothing, and a wall of solid colour
  // looks like data. Saying so beats drawing something that reads as a
  // measurement and is not.
  if (points.length < 6) {
    return (
      <p className="muted">
        Not enough readings yet to draw {label.toLowerCase()}. One is taken every five
        minutes.
      </p>
    );
  }

  const width = 640;
  const height = 120;
  const padding = { left: 34, right: 8, top: 8, bottom: 18 };

  const values = points.map((point) => point.value);
  let low = Math.min(...values);
  let high = Math.max(...values);
  // A flat series has no range to scale against, and dividing by zero draws
  // nothing at all. Centred rather than given a range above it: a machine that
  // has been perfectly steady should draw a line through the middle, not one
  // along the floor, which reads as "it stopped" rather than "it did not
  // change".
  if (high - low < 1) {
    low -= 0.5;
    high += 0.5;
  }
  // A little headroom, so the hottest reading is not drawn on the frame.
  const span = high - low;
  low -= span * 0.1;
  high += span * 0.1;

  const plotWidth = width - padding.left - padding.right;
  const plotHeight = height - padding.top - padding.bottom;
  const x = (i: number) => padding.left + (i / (points.length - 1)) * plotWidth;
  const y = (value: number) =>
    padding.top + plotHeight - ((value - low) / (high - low)) * plotHeight;

  const line = points.map((point, i) => `${x(i)},${y(point.value)}`).join(" ");
  const area = `${padding.left},${padding.top + plotHeight} ${line} ${
    padding.left + plotWidth
  },${padding.top + plotHeight}`;

  // Non-null by construction — the length check above guarantees both — but
  // stated so the compiler agrees rather than being told to be quiet.
  const first = new Date(points[0]?.time ?? "");
  const last = new Date(points[points.length - 1]?.time ?? "");
  const when = (date: Date) =>
    date.toLocaleDateString(undefined, { day: "numeric", month: "short" });

  return (
    <figure className="chart">
      <figcaption>
        {label}
        <span className="muted">
          {" "}
          — {Math.round(Math.min(...values))} to {Math.round(Math.max(...values))} {unit}
        </span>
      </figcaption>
      <svg
        viewBox={`0 0 ${width} ${height}`}
        role="img"
        aria-label={`${label} from ${when(first)} to ${when(last)}, between ${Math.round(
          Math.min(...values),
        )} and ${Math.round(Math.max(...values))} ${unit}`}
      >
        <polyline className="chart-area" points={area} />
        <polyline className="chart-line" points={line} />
        <text className="chart-tick" x={4} y={padding.top + 8}>
          {Math.round(high)}
        </text>
        <text className="chart-tick" x={4} y={padding.top + plotHeight}>
          {Math.round(low)}
        </text>
        <text className="chart-tick" x={padding.left} y={height - 4}>
          {when(first)}
        </text>
        <text className="chart-tick" x={width - padding.right} y={height - 4} textAnchor="end">
          {when(last)}
        </text>
      </svg>
    </figure>
  );
}
