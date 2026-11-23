package cli

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

const calStart = `{"auth_url":"https://accounts.example.test/o/oauth2/auth?state=x","session_id":"s1","expires_at":4102444800}`

func fastCalendarPoll(t *testing.T) *[]string {
	t.Helper()
	oldI, oldOpen := calendarPollInterval, calendarOpenBrowser
	calendarPollInterval = time.Millisecond
	opened := &[]string{}
	calendarOpenBrowser = func(u string) error { *opened = append(*opened, u); return nil }
	t.Cleanup(func() { calendarPollInterval, calendarOpenBrowser = oldI, oldOpen })
	return opened
}

func startRoute(provider, rule string, status int, resp string) m3Route {
	return m3Route{"POST", "/calendar/oauth/start", map[string]any{"provider": provider, "join_rule": rule}, status, resp}
}

func statusRoute(resp string) m3Route {
	return m3Route{"GET", "/calendar/oauth/status?session_id=s1", nil, 200, resp}
}

func TestCalendarConnectNoInputNeedsProvider(t *testing.T) {
	fastCalendarPoll(t)
	env, calls := m3Server(t, nil)
	if _, code := run(t, env, "calendar", "connect", "--no-input", "--json"); code != 2 || *calls != 0 {
		t.Fatalf("code=%d calls=%d", code, *calls)
	}
	if _, code := run(t, env, "calendar", "connect", "--no-input", "--provider", "yahoo"); code != 2 || *calls != 0 {
		t.Fatalf("bad provider: code=%d", code)
	}
	if _, code := run(t, env, "calendar", "connect", "--no-input", "--provider", "google", "--join-rule", "some"); code != 2 || *calls != 0 {
		t.Fatalf("bad join rule: code=%d", code)
	}
}

func TestCalendarConnectJSONConnected(t *testing.T) {
	opened := fastCalendarPoll(t)
	env, calls := m3Server(t, []m3Route{
		startRoute("google", "accepted", 200, calStart),
		statusRoute(`{"status":"pending","provider":"google"}`),
		statusRoute(`{"status":"connected","provider":"google","account_email":"a@example.test"}`),
	})
	out, code := run(t, env, "calendar", "connect", "--provider", "google", "--no-input", "--json")
	if code != 0 || *calls != 3 {
		t.Fatalf("code=%d calls=%d out=%s", code, *calls, out)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %s", out)
	}
	want := map[string]any{"provider": "google", "session_id": "s1", "status": "connected", "email": "a@example.test"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
	if len(*opened) != 0 {
		t.Fatal("--no-input opened a browser without --open")
	}
}

func TestCalendarConnectOpenAndHumanOutput(t *testing.T) {
	opened := fastCalendarPoll(t)
	env, calls := m3Server(t, []m3Route{
		startRoute("microsoft", "all", 200, calStart),
		statusRoute(`{"status":"connected","provider":"microsoft","account_email":"b@example.test"}`),
	})
	out, code := run(t, env, "calendar", "connect", "--provider", "microsoft", "--join-rule", "all", "--no-input", "--open")
	if code != 0 || *calls != 2 || out != "" {
		t.Fatalf("code=%d calls=%d stdout=%q", code, *calls, out)
	}
	if len(*opened) != 1 || !strings.HasPrefix((*opened)[0], "https://accounts.example.test/") {
		t.Fatalf("opened %v", *opened)
	}
}

func TestCalendarConnectNotAvailableExits3(t *testing.T) {
	fastCalendarPoll(t)
	env, calls := m3Server(t, []m3Route{startRoute("google", "accepted", 404, `{"message":"Not found"}`)})
	out, code := run(t, env, "calendar", "connect", "--provider", "google", "--no-input", "--json")
	if code != 3 || *calls != 1 || out != "" {
		t.Fatalf("code=%d out=%s", code, out)
	}
	if ee := calendarUnavailable("x").(*ExitError); !strings.Contains(ee.Hint, "calendar connect --own-app") {
		t.Fatalf("hint %q", ee.Hint)
	}
}

func TestCalendarConnectFailedExits1(t *testing.T) {
	fastCalendarPoll(t)
	env, calls := m3Server(t, []m3Route{
		startRoute("google", "accepted", 200, calStart),
		statusRoute(`{"status":"failed","provider":"google"}`),
	})
	out, code := run(t, env, "calendar", "connect", "--provider", "google", "--no-input", "--json")
	if code != 1 || *calls != 2 || out != "" {
		t.Fatalf("code=%d out=%s", code, out)
	}
}

func TestCalendarConnectNotSignedIn(t *testing.T) {
	fastCalendarPoll(t)
	if _, code := run(t, nil, "calendar", "connect", "--provider", "google", "--no-input"); code != 2 {
		t.Fatalf("code=%d", code)
	}
}

func TestCalendarConnectOwnAppFlags(t *testing.T) {
	env, calls := m3Server(t, nil)
	if _, code := run(t, env, "calendar", "connect", "--own-app", "--client-id", "x"); code != 2 || *calls != 0 {
		t.Fatalf("missing flags: code=%d", code)
	}
	if _, code := run(t, env, "calendar", "connect", "--own-app", "--provider", "google", "--client-id", "x", "--client-secret", "y", "--refresh-token", "z"); code != 2 || *calls != 0 {
		t.Fatalf("mixed flags: code=%d", code)
	}
}

func TestLoginSkipsCalendarPromptWhenNotInteractive(t *testing.T) {
	fastCalendarPoll(t)
	oldPick := calendarPick
	calendarPick = func(string, ...string) (int, error) { t.Fatal("prompted"); return -1, nil }
	t.Cleanup(func() { calendarPick = oldPick })
	for _, args := range [][]string{{"login", "--with-key", "--json"}, {"login", "--with-key"}, {"login", "--with-key", "--no-calendar"}} {
		env, calls := m3Server(t, []m3Route{{"GET", "/bots", nil, 200, `{"bots":[],"hasNextPage":false}`}})
		in, w, _ := os.Pipe()
		_, _ = w.WriteString("test-key\n")
		w.Close()
		oldIn := os.Stdin
		os.Stdin = in
		_, code := run(t, env, args...)
		os.Stdin = oldIn
		if code != 0 || *calls != 1 {
			t.Fatalf("%v: code=%d calls=%d", args, code, *calls)
		}
	}
}

// Microsoft is left out of menus until its sign-in app is live, but the flag still reaches it.
func TestCalendarMenuOffersGoogleOnly(t *testing.T) {
	if len(calendarProviders) != 1 || calendarProviders[0].id != "google" {
		t.Fatalf("menu providers = %+v, want Google only", calendarProviders)
	}
}
