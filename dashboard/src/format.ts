/**
 * Rendering helpers.
 *
 * The API returns bytes and seconds as numbers, deliberately — how they are
 * shown is a decision for whoever is showing them. This is where that decision
 * lives.
 */

export function bytes(value: number): string {
  const units = ["B", "kB", "MB", "GB", "TB"];
  let n = value;
  let unit = 0;
  while (n >= 1000 && unit < units.length - 1) {
    n /= 1000;
    unit += 1;
  }
  // One decimal place below 10, none above: "1.4 GB" is useful, "1.4 kB" is
  // noise, and "1447.2 MB" is unreadable.
  const rounded = n >= 10 || unit === 0 ? Math.round(n) : Math.round(n * 10) / 10;
  return `${rounded} ${units[unit]}`;
}

/**
 * Uptime, in words rather than digits.
 *
 * "3 days" is what somebody wants to know. "259 384 seconds" is the same fact
 * expressed so that they have to do arithmetic to use it.
 */
export function duration(seconds: number): string {
  if (seconds < 60) return "less than a minute";

  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return plural(minutes, "minute");

  const hours = Math.floor(minutes / 60);
  if (hours < 24) {
    const remainder = minutes % 60;
    return remainder === 0
      ? plural(hours, "hour")
      : `${plural(hours, "hour")}, ${plural(remainder, "minute")}`;
  }

  const days = Math.floor(hours / 24);
  const remainder = hours % 24;
  return remainder === 0
    ? plural(days, "day")
    : `${plural(days, "day")}, ${plural(remainder, "hour")}`;
}

function plural(n: number, word: string): string {
  return `${n} ${word}${n === 1 ? "" : "s"}`;
}

export function percentage(used: number, total: number): number {
  if (total <= 0) return 0;
  return Math.min(100, Math.max(0, Math.round((used / total) * 100)));
}
