// Reading daemon replies defensively, and saying what went wrong in words.
import { t } from './i18n.svelte'

/** A list from a reply: anything but an array (null from an empty Go slice, an older daemon
 *  that does not send the field, an empty body) is an empty list, never a crash. */
export const list = <T>(v: T[] | null | undefined): T[] => (Array.isArray(v) ? v : [])

/** True only when hosts answered and every one of them failed: no answers is not a failure.
 *  A host failed when it says ok: false or names an error. */
export const allFailed = (results: { ok?: boolean; error?: string }[] | null | undefined): boolean => {
  const rs = list(results)
  return rs.length > 0 && rs.every((r) => r.ok === false || !!r.error)
}

/** What went wrong, in words for the user (an API error's message, capitalised). */
export function errorText(e: unknown): string {
  const s = e instanceof Error ? e.message : String(e ?? '')
  // A fault in the page itself (TypeError, ReferenceError…) means nothing to the user: say what
  // to do and keep the details for whoever looks at the browser console.
  if (e instanceof Error && (e instanceof TypeError || e instanceof ReferenceError || e instanceof SyntaxError || e instanceof RangeError)) {
    console.error(e)
    return t('Something went wrong on this page. Reload it; if it happens again, the details are in the browser console.')
  }
  if (!s.trim()) return t('Something went wrong, without a reason given. Try again.')
  return s.charAt(0).toUpperCase() + s.slice(1)
}
