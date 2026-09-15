import { api } from './client';

export interface Me {
  name: string;
  role: 'admin' | 'viewer';
  can_write: boolean;
  /** "users" when named accounts are configured, "token" for the shared secret. */
  auth_mode: 'users' | 'token';
}

/** Exchange username/password for a session cookie. */
export const login = (username: string, password: string) =>
  api.post<{ name: string; role: string }>('/login', { username, password });

export const logout = () => api.post('/logout');

export const getMe = () => api.get<Me>('/me');
