import { NO_VALUE } from "@warehouse/ui-kit";

/** "2026-10-09 08:05:30Z" for an RFC 3339 instant; the raw text when unparseable. */
export function utcLabel(iso: string | undefined): string {
  if (!iso) return NO_VALUE;
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return iso;
  const d = new Date(t).toISOString();
  return `${d.slice(0, 10)} ${d.slice(11, 19)}Z`;
}

/** A rate in percent with one decimal: "97.0 %"; NO_VALUE while unseen. */
export function percent(rate: number | undefined): string {
  if (rate === undefined || rate === null) return NO_VALUE;
  return `${(rate * 100).toFixed(1)} %`;
}

/** Minutes with no unit decoration: "12"; NO_VALUE while unseen. */
export function minutes(m: number | undefined): string {
  if (m === undefined || m === null) return NO_VALUE;
  return String(m);
}

/** The id rule shared by vendor/node/po/transaction ids: 1..64 characters, no
 *  whitespace, control characters or "/". */
export function idProblem(what: string, id: string): string | null {
  if (id.length === 0) return `Enter a ${what}.`;
  if (id.length > 64) return `A ${what} is at most 64 characters.`;
  // eslint-disable-next-line no-control-regex
  if (/[\s/\u0000-\u001f\u007f]/.test(id)) return `A ${what} cannot contain spaces, control characters or '/'.`;
  return null;
}

/** Like idProblem, but an empty filter is fine ("any"). */
export function optionalIdProblem(what: string, id: string): string | null {
  return id.length === 0 ? null : idProblem(what, id);
}
