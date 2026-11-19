// Reading daemon replies defensively.

/** A list from a reply: anything but an array (null from an empty Go slice, an older daemon
 *  that does not send the field, an empty body) is an empty list, never a crash. */
export const list = <T>(v: T[] | null | undefined): T[] => (Array.isArray(v) ? v : [])
