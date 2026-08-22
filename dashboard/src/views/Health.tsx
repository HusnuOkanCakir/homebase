import { useEffect, useState } from "react";
import { api, type SystemInfo, type ThermalHistory } from "../api";
import { TimeChart } from "../components/TimeChart";
import { bytes, duration } from "../format";

/**
 * How this server has been doing.
 *
 * Two things are being answered and they are not the same question, so they are
 * not given the same treatment. **What is it doing now** is a number — five of
 * them, along the top, because a chart of one instant is a chart of nothing.
 * **How has it been** is a shape, and every question worth asking about it is a
 * comparison: hotter than last week, does memory climb until something is
 * restarted, does the fan follow the temperature or its own mind.
 *
 * One measurement per chart, never two scales on one plot — see TimeChart. The
 * charts are stacked rather than gridded because they share an x-axis: reading
 * downward at one moment is the whole point, and a two-column grid breaks that
 * for no gain but density.
 */

const RANGES = [
  { days: 1, label: "Day" },
  { days: 7, label: "Week" },
  { days: 30, label: "Month" },
] as const;

export function Health({ system }: { system: SystemInfo }) {
  const [history, setHistory] = useState<ThermalHistory | null>(null);
  const [days, setDays] = useState(1);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    let current = true;
    // Cleared when the new range arrives rather than when it is asked for.
    // Clearing it up front shows the previous chart with no error for as long as
    // the request takes, and then puts the error back — which reads as a fault
    // that comes and goes rather than one that never went away.
    api
      .systemHistory(days)
      .then((result) => {
        if (!current) return;
        setHistory(result);
        setFailed(false);
      })
      .catch(() => {
        if (current) setFailed(true);
      });
    return () => {
      current = false;
    };
  }, [days]);

  const samples = history?.samples ?? [];
  const times = samples.map((sample) => sample.time);
  const column = <K extends keyof (typeof samples)[number]>(key: K) =>
    samples.map((sample) => (sample[key] ?? null) as number | null);

  return (
    <section className="card card-wide">
      <div className="row row-spread">
        <h3>How this server is doing</h3>
        <div className="row">
          {RANGES.map((range) => (
            <button
              key={range.days}
              className={range.days === days ? "quiet selected" : "quiet"}
              onClick={() => setDays(range.days)}
            >
              {range.label}
            </button>
          ))}
        </div>
      </div>

      {/* Now, as numbers. A chart cannot say what a single instant is, and this
          is the row somebody glances at to decide whether to read further. */}
      <dl className="tiles">
        <div className="tile">
          <dt>Processor</dt>
          <dd>
            {system.load_average[0].toFixed(2)} <span className="unit">load</span>
          </dd>
        </div>
        {/* Memory is deliberately not here. The bars above this say it better —
            a proportion of a total, next to the disk, which is the comparison
            somebody is actually making. Two readings of the same number on one
            screen invite a search for the difference between them. */}
        {system.temperature.celsius !== null ? (
          <div className="tile">
            <dt>Temperature</dt>
            <dd>
              {system.temperature.celsius} <span className="unit">°C</span>
            </dd>
          </div>
        ) : null}
        {system.fan.rpm !== null ? (
          <div className="tile">
            <dt>Fan</dt>
            <dd>
              {system.fan.rpm} <span className="unit">rpm</span>
            </dd>
          </div>
        ) : null}
        <div className="tile">
          <dt>Up</dt>
          <dd className="unit">{duration(system.uptime_seconds)}</dd>
        </div>
      </dl>

      {failed || samples.length < 6 ? (
        <p className="muted">
          A reading is taken every five minutes. There is not enough recorded yet to
          draw the last {days === 1 ? "day" : days === 7 ? "week" : "month"}.
        </p>
      ) : (
        <>
          {/* 0–100 fixed, not scaled to what was reached. A processor that
              peaked at 4% would otherwise draw an alarming mountain. */}
          <TimeChart
            title="Processor"
            unit="%"
            times={times}
            max={100}
            format={(value) => `${Math.round(value)}%`}
            series={[{ label: "Busy", slot: 1, values: column("cpu_percent") }]}
          />
          <TimeChart
            title="Memory in use"
            unit="%"
            times={times}
            max={100}
            format={(value) => `${Math.round(value)}%`}
            series={[{ label: "Used", slot: 1, values: column("memory_percent") }]}
          />
          {/* Two series, one axis, because they share a unit. Two axes would
              invent a relationship between upload and download that is not
              there. */}
          <TimeChart
            title="Network"
            unit="bytes per second"
            zero
            times={times}
            format={(value) => `${bytes(value)}/s`}
            axisFormat={(value) => bytes(value)}
            series={[
              { label: "Down", slot: 1, values: column("download_bytes_per_second") },
              { label: "Up", slot: 2, values: column("upload_bytes_per_second") },
            ]}
          />
          <TimeChart
            title="Temperature"
            unit="°C"
            times={times}
            format={(value) => `${Math.round(value)} °C`}
            series={[{ label: "Temperature", slot: 1, values: column("celsius") }]}
          />
          <TimeChart
            title="Fan"
            unit="rpm"
            times={times}
            format={(value) => `${Math.round(value)} rpm`}
            series={[{ label: "Fan", slot: 1, values: column("fan_rpm") }]}
          />

          {/* The relief the contrast check asks for, and the thing somebody
              wants anyway once a chart has raised a question. */}
          <p className="muted">
            Every reading is in a plain file at <code>/var/log/homebase/thermal.csv</code>,
            readable without Homebase — one row every five minutes, for anything a
            chart cannot answer.
          </p>
        </>
      )}
    </section>
  );
}
