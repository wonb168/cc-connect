package core

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// testAuthServer builds a management server through the real buildHandler, so
// routing and middleware match production rather than a hand-wired mux.
func testAuthServer(t *testing.T, token string, users []ManagementUserInfo) (*ManagementServer, *httptest.Server) {
	t.Helper()
	agent := &stubAgent{}
	e := NewEngine("test-project", agent, nil, "", LangEnglish)
	e.sessions = NewSessionManager("")

	mgmt := NewManagementServer(0, token, nil)
	mgmt.RegisterEngine("test-project", e)
	if len(users) > 0 {
		mgmt.SetManagementUsers(users, 0)
	}
	ts := httptest.NewServer(mgmt.buildHandler(http.NewServeMux()))
	t.Cleanup(func() {
		ts.Close()
		mgmt.auth.stop()
	})
	return mgmt, ts
}

// newJarClient returns a client that keeps cookies, like a browser.
func newJarClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	return &http.Client{Jar: jar}
}

func doJSON(t *testing.T, c *http.Client, method, url string, body any) (int, mgmtResponse) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req, err := http.NewRequest(method, url, &buf)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	var r mgmtResponse
	_ = json.NewDecoder(resp.Body).Decode(&r)
	return resp.StatusCode, r
}

func mustHash(t *testing.T, pw string) string {
	t.Helper()
	h, err := HashManagementPassword(pw)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	return h
}

// ── Backward compatibility ────────────────────────────────────

// The shared token must keep working exactly as before when no accounts exist.
func TestMgmtAuth_LegacyTokenStillWorks(t *testing.T) {
	_, ts := testAuthServer(t, "secret-token", nil)
	c := &http.Client{}

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/status", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("GET status: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 for legacy token", resp.StatusCode)
	}

	// Wrong token still rejected.
	req2, _ := http.NewRequest("GET", ts.URL+"/api/v1/status", nil)
	req2.Header.Set("Authorization", "Bearer wrong")
	resp2, err := c.Do(req2)
	if err != nil {
		t.Fatalf("GET status with wrong token: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token = %d, want 401", resp2.StatusCode)
	}
}

// Empty token with no users keeps the historical "no auth" behaviour.
func TestMgmtAuth_NoTokenNoUsersIsOpen(t *testing.T) {
	_, ts := testAuthServer(t, "", nil)
	code, _ := doJSON(t, &http.Client{}, "GET", ts.URL+"/api/v1/status", nil)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200 when no auth configured", code)
	}
}

// Regression guard: an empty shared token must NOT bypass configured accounts.
func TestMgmtAuth_NoTokenWithUsersIsClosed(t *testing.T) {
	_, ts := testAuthServer(t, "", []ManagementUserInfo{
		{Name: "alice", PasswordHash: mustHash(t, "correct-horse"), Role: RoleAdmin},
	})
	code, _ := doJSON(t, &http.Client{}, "GET", ts.URL+"/api/v1/status", nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 — empty token must not bypass accounts", code)
	}
}

// ── Login flow ────────────────────────────────────────────────

func TestMgmtAuth_LoginAndAccess(t *testing.T) {
	_, ts := testAuthServer(t, "", []ManagementUserInfo{
		{Name: "alice", PasswordHash: mustHash(t, "correct-horse"), Role: RoleAdmin},
	})
	c := newJarClient(t)

	code, r := doJSON(t, c, "POST", ts.URL+"/api/v1/login",
		map[string]string{"username": "alice", "password": "correct-horse"})
	if code != http.StatusOK || !r.OK {
		t.Fatalf("login = %d ok=%v err=%q, want 200", code, r.OK, r.Error)
	}

	// The session cookie now authenticates subsequent requests.
	code, _ = doJSON(t, c, "GET", ts.URL+"/api/v1/status", nil)
	if code != http.StatusOK {
		t.Fatalf("status after login = %d, want 200", code)
	}

	// /me reports identity.
	code, r = doJSON(t, c, "GET", ts.URL+"/api/v1/me", nil)
	if code != http.StatusOK {
		t.Fatalf("me = %d, want 200", code)
	}
	var me struct {
		Name     string `json:"name"`
		Role     string `json:"role"`
		CanWrite bool   `json:"can_write"`
	}
	if err := json.Unmarshal(r.Data, &me); err != nil {
		t.Fatalf("decode me: %v", err)
	}
	if me.Name != "alice" || me.Role != RoleAdmin || !me.CanWrite {
		t.Fatalf("me = %+v, want alice/admin/can_write", me)
	}
}

func TestMgmtAuth_LoginWrongPassword(t *testing.T) {
	_, ts := testAuthServer(t, "", []ManagementUserInfo{
		{Name: "alice", PasswordHash: mustHash(t, "correct-horse"), Role: RoleAdmin},
	})
	c := newJarClient(t)

	code, _ := doJSON(t, c, "POST", ts.URL+"/api/v1/login",
		map[string]string{"username": "alice", "password": "wrong"})
	if code != http.StatusUnauthorized {
		t.Fatalf("bad password = %d, want 401", code)
	}
	// No session was established.
	code, _ = doJSON(t, c, "GET", ts.URL+"/api/v1/status", nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("status after failed login = %d, want 401", code)
	}
}

// Unknown user and wrong password must be indistinguishable to the client.
func TestMgmtAuth_UnknownUserSameResponse(t *testing.T) {
	_, ts := testAuthServer(t, "", []ManagementUserInfo{
		{Name: "alice", PasswordHash: mustHash(t, "correct-horse"), Role: RoleAdmin},
	})
	c := newJarClient(t)

	_, rWrong := doJSON(t, c, "POST", ts.URL+"/api/v1/login",
		map[string]string{"username": "alice", "password": "wrong"})
	_, rUnknown := doJSON(t, c, "POST", ts.URL+"/api/v1/login",
		map[string]string{"username": "nobody", "password": "wrong"})

	if rWrong.Error != rUnknown.Error {
		t.Fatalf("error messages differ (user enumeration): %q vs %q", rWrong.Error, rUnknown.Error)
	}
}

func TestMgmtAuth_Logout(t *testing.T) {
	_, ts := testAuthServer(t, "", []ManagementUserInfo{
		{Name: "alice", PasswordHash: mustHash(t, "correct-horse"), Role: RoleAdmin},
	})
	c := newJarClient(t)

	doJSON(t, c, "POST", ts.URL+"/api/v1/login",
		map[string]string{"username": "alice", "password": "correct-horse"})
	if code, _ := doJSON(t, c, "POST", ts.URL+"/api/v1/logout", nil); code != http.StatusOK {
		t.Fatalf("logout = %d, want 200", code)
	}
	if code, _ := doJSON(t, c, "GET", ts.URL+"/api/v1/status", nil); code != http.StatusUnauthorized {
		t.Fatalf("status after logout = %d, want 401", code)
	}
}

// The session cookie must be HttpOnly so XSS cannot read it.
func TestMgmtAuth_SessionCookieIsHttpOnly(t *testing.T) {
	_, ts := testAuthServer(t, "", []ManagementUserInfo{
		{Name: "alice", PasswordHash: mustHash(t, "correct-horse"), Role: RoleAdmin},
	})

	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(map[string]string{"username": "alice", "password": "correct-horse"})
	resp, err := http.Post(ts.URL+"/api/v1/login", "application/json", &buf)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer resp.Body.Close()

	for _, ck := range resp.Cookies() {
		if ck.Name == sessionCookieName {
			if !ck.HttpOnly {
				t.Fatal("session cookie is not HttpOnly")
			}
			if ck.Value == "" {
				t.Fatal("session cookie is empty")
			}
			return
		}
	}
	t.Fatalf("no %s cookie set on login", sessionCookieName)
}

// A forged cookie with a valid-looking id but bad signature must be rejected.
func TestMgmtAuth_ForgedCookieRejected(t *testing.T) {
	mgmt, ts := testAuthServer(t, "", []ManagementUserInfo{
		{Name: "alice", PasswordHash: mustHash(t, "correct-horse"), Role: RoleAdmin},
	})

	// Real session, then tamper with the signature.
	value, _, err := mgmt.auth.issueSession(Principal{Name: "alice", Role: RoleAdmin})
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}
	id, _, _ := strings.Cut(value, ".")

	for _, forged := range []string{
		id + ".bogus-signature",
		id,
		"deadbeef.deadbeef",
	} {
		req, _ := http.NewRequest("GET", ts.URL+"/api/v1/status", nil)
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: forged})
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("forged cookie %q = %d, want 401", forged, resp.StatusCode)
		}
	}
}

func TestMgmtAuth_ExpiredSessionRejected(t *testing.T) {
	mgmt, ts := testAuthServer(t, "", []ManagementUserInfo{
		{Name: "alice", PasswordHash: mustHash(t, "correct-horse"), Role: RoleAdmin},
	})
	mgmt.auth.mu.Lock()
	mgmt.auth.ttl = -time.Second // already expired on issue
	mgmt.auth.mu.Unlock()

	value, _, err := mgmt.auth.issueSession(Principal{Name: "alice", Role: RoleAdmin})
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}
	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/status", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: value})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expired session = %d, want 401", resp.StatusCode)
	}
}

// ── Roles ─────────────────────────────────────────────────────

func TestMgmtAuth_ViewerIsReadOnly(t *testing.T) {
	_, ts := testAuthServer(t, "", []ManagementUserInfo{
		{Name: "bob", PasswordHash: mustHash(t, "hunter2222"), Role: RoleViewer},
	})
	c := newJarClient(t)

	if code, _ := doJSON(t, c, "POST", ts.URL+"/api/v1/login",
		map[string]string{"username": "bob", "password": "hunter2222"}); code != http.StatusOK {
		t.Fatalf("viewer login = %d, want 200", code)
	}

	// Reads allowed.
	if code, _ := doJSON(t, c, "GET", ts.URL+"/api/v1/status", nil); code != http.StatusOK {
		t.Fatalf("viewer GET = %d, want 200", code)
	}

	// Every mutating method rejected.
	for _, tc := range []struct{ method, path string }{
		{"POST", "/api/v1/restart"},
		{"PATCH", "/api/v1/projects/test-project"},
		{"DELETE", "/api/v1/cron/anything"},
		{"POST", "/api/v1/providers"},
	} {
		code, _ := doJSON(t, c, tc.method, ts.URL+tc.path, map[string]string{})
		if code != http.StatusForbidden {
			t.Fatalf("viewer %s %s = %d, want 403", tc.method, tc.path, code)
		}
	}

	// /me advertises the restriction.
	_, r := doJSON(t, c, "GET", ts.URL+"/api/v1/me", nil)
	var me struct {
		Role     string `json:"role"`
		CanWrite bool   `json:"can_write"`
	}
	if err := json.Unmarshal(r.Data, &me); err != nil {
		t.Fatalf("decode me: %v", err)
	}
	if me.Role != RoleViewer || me.CanWrite {
		t.Fatalf("me = %+v, want viewer/can_write=false", me)
	}
}

func TestMgmtAuth_AdminCanWrite(t *testing.T) {
	_, ts := testAuthServer(t, "", []ManagementUserInfo{
		{Name: "alice", PasswordHash: mustHash(t, "correct-horse"), Role: RoleAdmin},
	})
	c := newJarClient(t)
	doJSON(t, c, "POST", ts.URL+"/api/v1/login",
		map[string]string{"username": "alice", "password": "correct-horse"})

	// Not 403: admin passes the role gate (the handler may still 4xx on body).
	code, _ := doJSON(t, c, "PATCH", ts.URL+"/api/v1/projects/test-project",
		map[string]any{"language": "en"})
	if code == http.StatusForbidden {
		t.Fatal("admin PATCH was forbidden, want it to pass the role gate")
	}
}

// ── Per-user API tokens ───────────────────────────────────────

func TestMgmtAuth_PerUserToken(t *testing.T) {
	_, ts := testAuthServer(t, "", []ManagementUserInfo{
		{Name: "script", Token: "script-token-abc", Role: RoleViewer},
	})

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/status", nil)
	req.Header.Set("Authorization", "Bearer script-token-abc")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("per-user token = %d, want 200", resp.StatusCode)
	}

	// The token carries its role: viewer still cannot write.
	req2, _ := http.NewRequest("POST", ts.URL+"/api/v1/restart", strings.NewReader("{}"))
	req2.Header.Set("Authorization", "Bearer script-token-abc")
	req2.Header.Set("Content-Type", "application/json")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer token POST = %d, want 403", resp2.StatusCode)
	}
}

// ── Login endpoint behaviour ──────────────────────────────────

func TestMgmtAuth_LoginRateLimited(t *testing.T) {
	_, ts := testAuthServer(t, "", []ManagementUserInfo{
		{Name: "alice", PasswordHash: mustHash(t, "correct-horse"), Role: RoleAdmin},
	})
	c := newJarClient(t)

	limited := false
	for i := 0; i < loginRateMaxTries+5; i++ {
		code, _ := doJSON(t, c, "POST", ts.URL+"/api/v1/login",
			map[string]string{"username": "alice", "password": "wrong"})
		if code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatalf("no 429 after %d bad logins — brute force is unthrottled", loginRateMaxTries+5)
	}
}

func TestMgmtAuth_LoginWithoutUsersConfigured(t *testing.T) {
	_, ts := testAuthServer(t, "tok", nil)
	code, _ := doJSON(t, &http.Client{}, "POST", ts.URL+"/api/v1/login",
		map[string]string{"username": "alice", "password": "x"})
	if code != http.StatusNotImplemented {
		t.Fatalf("login without users = %d, want 501", code)
	}
}

func TestMgmtAuth_LoginRejectsGET(t *testing.T) {
	_, ts := testAuthServer(t, "", []ManagementUserInfo{
		{Name: "alice", PasswordHash: mustHash(t, "correct-horse"), Role: RoleAdmin},
	})
	code, _ := doJSON(t, &http.Client{}, "GET", ts.URL+"/api/v1/login", nil)
	if code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /login = %d, want 405", code)
	}
}

// ── Unit-level checks ─────────────────────────────────────────

func TestMgmtAuth_VerifyPasswordRejectsTokenOnlyUser(t *testing.T) {
	a := newManagementAuth(0)
	defer a.stop()
	a.SetUsers([]ManagementUserInfo{{Name: "script", Token: "t", Role: RoleAdmin}})

	// A user with no password hash must never authenticate by password,
	// including with the empty password.
	for _, pw := range []string{"", "anything"} {
		if p := a.VerifyPassword("script", pw); p != nil {
			t.Fatalf("token-only user authenticated with password %q", pw)
		}
	}
}

func TestMgmtAuth_RoleDefaultsToAdmin(t *testing.T) {
	a := newManagementAuth(0)
	defer a.stop()
	a.SetUsers([]ManagementUserInfo{{Name: "alice", PasswordHash: mustHash(t, "correct-horse")}})

	p := a.VerifyPassword("alice", "correct-horse")
	if p == nil || p.Role != RoleAdmin {
		t.Fatalf("principal = %+v, want role admin by default", p)
	}
}

func TestMgmtAuth_UsernameCaseInsensitive(t *testing.T) {
	a := newManagementAuth(0)
	defer a.stop()
	a.SetUsers([]ManagementUserInfo{{Name: "Alice", PasswordHash: mustHash(t, "correct-horse")}})

	p := a.VerifyPassword("ALICE", "correct-horse")
	if p == nil {
		t.Fatal("login should be case-insensitive on username")
	}
	if p.Name != "Alice" {
		t.Fatalf("principal name = %q, want configured casing %q", p.Name, "Alice")
	}
}
