import { create } from 'zustand';
import { api } from '@/api/client';
import { getMe, logout as apiLogout, type Me } from '@/api/auth';

interface AuthState {
  token: string;
  serverUrl: string;
  isAuthenticated: boolean;
  /** Identity from /me. Null in legacy token mode until resolved. */
  me: Me | null;
  /** False for viewer accounts — hide write controls. */
  canWrite: boolean;
  login: (token: string, serverUrl?: string) => void;
  loginSession: (me: Me) => void;
  logout: () => void;
  refreshMe: () => Promise<void>;
  init: () => void;
}

export const useAuthStore = create<AuthState>((set) => ({
  token: '',
  serverUrl: '',
  isAuthenticated: false,
  me: null,
  // Default true so token-mode installs (no accounts) keep full access.
  canWrite: true,
  login: (token: string, serverUrl?: string) => {
    api.setToken(token);
    localStorage.setItem('cc_token', token);
    if (serverUrl) localStorage.setItem('cc_server_url', serverUrl);
    set({ token, serverUrl: serverUrl || '', isAuthenticated: true });
  },
  // Password login: the session lives in an HttpOnly cookie, so there is no
  // token to keep. Only a non-sensitive flag is persisted, to restore the
  // logged-in view on reload; the cookie is what actually authenticates.
  loginSession: (me: Me) => {
    api.setToken('');
    localStorage.removeItem('cc_token');
    localStorage.setItem('cc_session', '1');
    set({ token: '', isAuthenticated: true, me, canWrite: me.can_write });
  },
  logout: () => {
    void apiLogout().catch(() => { /* already invalid server-side */ });
    api.setToken('');
    localStorage.removeItem('cc_token');
    localStorage.removeItem('cc_server_url');
    localStorage.removeItem('cc_session');
    set({ token: '', serverUrl: '', isAuthenticated: false, me: null, canWrite: true });
  },
  refreshMe: async () => {
    try {
      const me = await getMe();
      set({ me, canWrite: me.can_write });
    } catch {
      // Token mode without /me support, or not authenticated yet.
    }
  },
  init: () => {
    const token = localStorage.getItem('cc_token') || '';
    const serverUrl = localStorage.getItem('cc_server_url') || '';
    const hasSession = localStorage.getItem('cc_session') === '1';
    if (token) {
      api.setToken(token);
      set({ token, serverUrl, isAuthenticated: true });
    } else if (hasSession) {
      // Trust the cookie provisionally; any 401 clears it via onUnauthorized.
      set({ serverUrl, isAuthenticated: true });
    }
  },
}));
