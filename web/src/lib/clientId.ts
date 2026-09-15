// Per-browser and per-tab identity for the dashboard.
//
// The bridge server keys its adapter registry by platform name and closes the
// existing connection whenever a new one registers under the same name (see
// core/bridge.go). Every dashboard used to register as plain "web", so a second
// browser — or a second tab — evicted the first, which then reconnected and
// evicted the second, forever.
//
// The adapter name is therefore per-tab, so connections never collide. The
// session key stays per-browser, so closing and reopening a tab continues the
// same agent conversation instead of starting a fresh one.

const BROWSER_KEY = 'cc_client_id';
const TAB_KEY = 'cc_tab_id';

function randomId(): string {
  if (globalThis.crypto?.randomUUID) return globalThis.crypto.randomUUID().slice(0, 8);
  return Math.random().toString(36).slice(2, 10);
}

function persistentId(store: Storage, key: string): string {
  try {
    const existing = store.getItem(key);
    if (existing) return existing;
    const id = randomId();
    store.setItem(key, id);
    return id;
  } catch {
    // Storage blocked (private mode, embedded webview): fall back to an id that
    // lives for this page load only.
    return randomId();
  }
}

/** Stable per-browser id; survives tab close and browser restart. */
export const browserId = persistentId(localStorage, BROWSER_KEY);

/** Stable per-tab id; survives reload, unique across tabs. */
export const tabId = persistentId(sessionStorage, TAB_KEY);

/** Bridge adapter name — unique per tab so connections never evict each other. */
export const bridgePlatformName = `web-${browserId}-${tabId}`;

/**
 * Session key for this browser's own conversation in a project.
 * Format is `{platform}:{scope}:{user}`. The platform segment is the adapter
 * name minus the tab suffix; BridgeServer.platformFromSessionKey falls back to
 * prefix matching on that, so server-initiated pushes (cron, heartbeat) reach
 * one of this browser's live tabs.
 */
export function makeWebSessionKey(projectName: string): string {
  return `web-${browserId}:${projectName}:${browserId}`;
}
