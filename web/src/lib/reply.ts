// Reading daemon replies defensively.

/** A list from a reply: anything but an array (null from an empty Go slice, an older daemon
 *  that does not send the field, an empty body) is an empty list, never a crash. */
export const list = <T>(v: T[] | null | undefined): T[] => (Array.isArray(v) ? v : [])

/** True only when hosts answered and every one of them failed: no answers is not a failure.
 *  A host failed when it says ok: false or names an error. */
export const allFailed = (results: { ok?: boolean; error?: string }[] | null | undefined): boolean => {
  const rs = list(results)
  return rs.length > 0 && rs.every((r) => r.ok === false || !!r.error)
}
