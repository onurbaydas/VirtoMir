// Versions as git refs, for links to what changed between two of them.

/**
 * gitRef turns a VirtoMir version into something GitHub can compare: the commit of a
 * `git describe` version (v1.1.1-29-g0248c1c → 0248c1c), else the tag (v1.1.1). A version
 * built from changed files (-dirty), "dev" or anything else unexpected gives "".
 */
export function gitRef(version: string): string {
  const v = version.trim()
  if (/-dirty$/.test(v)) return ''
  const described = v.match(/^v?\d+\.\d+\.\d+(?:-[0-9A-Za-z.]+)*?-\d+-g([0-9a-f]{7,40})$/)
  if (described) return described[1]
  if (/^v\d+\.\d+\.\d+(?:-[0-9A-Za-z.]+)?$/.test(v)) return v
  return ''
}

/** compareURL is GitHub's page of the changes from one version to another, or "" when unknown. */
export function compareURL(repo: string, from: string, to: string): string {
  const a = gitRef(from), b = gitRef(to)
  if (!a || !b || a === b || !/^[\w.-]+\/[\w.-]+$/.test(repo)) return ''
  return `https://github.com/${repo}/compare/${a}...${b}`
}
