const reservedRepositoryOwners = new Set([
  "_repos",
  "admin",
  "api",
  "assets",
  "auth",
  "bootstrap",
  "changes",
  "issues",
  "livez",
  "login",
  "metrics",
  "notifications",
  "orgs",
  "readyz",
  "repos",
  "settings",
  "user",
  "users",
]);

function segment(value: string) {
  return encodeURIComponent(value);
}

export function isRepositoryRootOwner(owner: string) {
  return Boolean(owner) && !reservedRepositoryOwners.has(owner.toLowerCase());
}

export function repositoryRootPath(owner: string, repository: string) {
  const prefix = isRepositoryRootOwner(owner) ? "" : "/_repos";
  return `${prefix}/${segment(owner)}/${segment(repository)}`;
}

export function repositoryIssuePathForNames(owner: string, repository: string, number?: number | string) {
  const base = `${repositoryRootPath(owner, repository)}/issues`;
  return number === undefined ? base : `${base}/${segment(String(number))}`;
}

export function repositoryChangePathForNames(owner: string, repository: string, change?: string) {
  const base = `${repositoryRootPath(owner, repository)}/changes`;
  return change === undefined ? base : `${base}/${segment(change)}`;
}

export function organizationChangePath(owner: string) {
  return `/orgs/${segment(owner)}/changes`;
}

export function isCanonicalRepositoryReadPath(pathname: string) {
  if (/^\/_repos\/[^/]+\/[^/]+\/?$/.test(pathname)) return true;
  if (/^\/_repos\/[^/]+\/[^/]+\/(?:issues(?:\/[1-9]\d*)?|changes(?:\/[^/]+)?)\/?$/.test(pathname)) return true;
  if (/^\/[^/]+\/[^/]+\/(?:issues(?:\/[1-9]\d*)?|changes(?:\/[^/]+)?)\/?$/.test(pathname)) return true;
  const match = pathname.match(/^\/([^/]+)\/([^/]+)\/?$/);
  return Boolean(match && isRepositoryRootOwner(match[1]));
}

export function isPublicUserProfilePath(pathname: string) {
  return /^\/users\/[^/]+(?:\/issues)?\/?$/.test(pathname);
}

export function isIssueFeaturePath(pathname: string) {
  if (/^\/issues(?:\/|$)/.test(pathname)) return true;
  if (/^\/_repos\/[^/]+\/[^/]+\/issues(?:\/|$)/.test(pathname)) return true;
  const match = pathname.match(/^\/([^/]+)\/[^/]+\/issues(?:\/|$)/);
  return Boolean(match && isRepositoryRootOwner(match[1]));
}

export function isChangeFeaturePath(pathname: string) {
  if (/^\/changes(?:\/|$)/.test(pathname) || /^\/orgs\/[^/]+\/changes(?:\/|$)/.test(pathname)) return true;
  if (/^\/_repos\/[^/]+\/[^/]+\/changes(?:\/|$)/.test(pathname)) return true;
  const match = pathname.match(/^\/([^/]+)\/[^/]+\/changes(?:\/|$)/);
  return Boolean(match && isRepositoryRootOwner(match[1]));
}
