//go:build e2e

package e2e

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

const deliveryTimeout = 10 * time.Second

func TestHealthz(t *testing.T) {
	s := startServer(t)
	if res := s.request(t, http.MethodGet, "/healthz", nil, nil); res.status != http.StatusOK {
		t.Fatalf("status = %d", res.status)
	}
}

func TestFanOutToEveryUserInOrder(t *testing.T) {
	s := startServer(t)
	for _, sc := range []scope{private, public} {
		t.Run(sc.String(), func(t *testing.T) {
			users := s.connectMany(t, sc, "orders", 50)
			s.awaitSubscribed(t, users...)

			const messages = 25
			for i := range messages {
				s.mustPublish(t, sc, "orders", message(map[string]any{"seq": i, "order": fmt.Sprintf("order-%d", i)}))
			}
			for idx, u := range users {
				for i, raw := range u.collect(t, messages, deliveryTimeout) {
					got := decode[struct {
						Seq   int
						Order string
					}](t, raw)
					if got.Seq != i || got.Order != fmt.Sprintf("order-%d", i) {
						t.Fatalf("user %d message %d = %s", idx, i, raw)
					}
				}
				u.expectNothing(t, 0)
			}
		})
	}
}

func TestPayloadIsDeliveredByteForByte(t *testing.T) {
	s := startServer(t, withEnv("MAX_BODY_BYTES", "65536"))
	u := s.connect(t, public, "bytes")
	s.awaitSubscribed(t, u)

	payloads := []string{
		`{"id":"order-1"}`,
		`{ "spaced" : [ 1 , 2 ] }`,
		"{\n  \"pretty\": true\n}",
		`[1,"two",{"three":3},null,true,false,-1.5e-10]`,
		`"just a string"`,
		`12345`,
		`null`,
		`{"unicode":"日本語 – émoji 🚀 – עברית","escapes":"\u0000\u001f\"\\\/"}`,
		`{"html":"<script>alert(1)</script>"}`,
		`{"deep":` + strings.Repeat(`{"a":`, 200) + `1` + strings.Repeat(`}`, 200) + `}`,
		`{"big":"` + strings.Repeat("x", 65536-10) + `"}`,
	}
	for _, p := range payloads {
		s.mustPublish(t, public, "bytes", []byte(p))
	}
	for i, got := range u.collect(t, len(payloads), deliveryTimeout) {
		if !bytes.Equal(got, []byte(strings.TrimSpace(payloads[i]))) {
			t.Errorf("payload %d altered:\n got %.120q\nwant %.120q", i, got, payloads[i])
		}
	}
}

func TestEveryMessageIsItsOwnFrame(t *testing.T) {
	s := startServer(t)
	u := s.connect(t, private, "burst")
	s.awaitSubscribed(t, u)

	const burst = 500
	done := make(chan struct{})
	for p := range 10 {
		go func() {
			defer func() { done <- struct{}{} }()
			for i := range burst / 10 {
				if status := s.publish(t, private, "burst", message(map[string]any{"p": p, "i": i})); status != http.StatusAccepted {
					t.Errorf("publish status %d", status)
				}
			}
		}()
	}
	for range 10 {
		<-done
	}

	msgs := u.collect(t, burst, deliveryTimeout)
	if frames := u.frames.Load(); frames != burst {
		t.Fatalf("received %d messages in %d frames: documents were concatenated", len(msgs), frames)
	}
}

func TestDefaultChannel(t *testing.T) {
	s := startServer(t)
	implicit := s.connect(t, public, "")
	explicit := s.connect(t, public, "default")
	other := s.connect(t, public, "other")
	s.awaitSubscribed(t, implicit, explicit, other)

	s.mustPublish(t, public, "", []byte(`{"to":"default"}`))
	s.mustPublish(t, public, "default", []byte(`{"to":"default-explicit"}`))

	for _, u := range []*user{implicit, explicit} {
		if got := string(u.next(t, deliveryTimeout)); got != `{"to":"default"}` {
			t.Errorf("got %s", got)
		}
		if got := string(u.next(t, deliveryTimeout)); got != `{"to":"default-explicit"}` {
			t.Errorf("got %s", got)
		}
	}
	other.expectNothing(t, 200*time.Millisecond)
}

func TestChannelAndScopeIsolation(t *testing.T) {
	s := startServer(t)
	const channels, perChannel = 15, 4

	type group struct {
		scope   scope
		channel string
		users   []*user
	}
	var groups []group
	var all []*user
	for _, sc := range []scope{private, public} {
		for c := range channels {
			name := fmt.Sprintf("tenant-%d", c)
			users := s.connectMany(t, sc, name, perChannel)
			groups = append(groups, group{sc, name, users})
			all = append(all, users...)
		}
	}
	s.awaitSubscribed(t, all...)

	for _, g := range groups {
		s.mustPublish(t, g.scope, g.channel, message(map[string]any{"scope": g.scope.String(), "channel": g.channel}))
	}
	for _, g := range groups {
		want := string(message(map[string]any{"scope": g.scope.String(), "channel": g.channel}))
		for _, u := range g.users {
			if got := string(u.next(t, deliveryTimeout)); got != want {
				t.Fatalf("%s/%s user got %s", g.scope, g.channel, got)
			}
		}
	}
	time.Sleep(200 * time.Millisecond)
	for _, u := range all {
		u.expectNothing(t, 0)
	}
}

func TestChannelNamesAreOpaque(t *testing.T) {
	s := startServer(t)
	names := []string{
		"with space",
		"日本語",
		"emoji-🚀",
		"a/b/c",
		"../../etc/passwd",
		"%2F",
		"semi;colon",
		"amp&ersand=1",
		"quote\"s'",
		"CaseSensitive",
		"casesensitive",
		strings.Repeat("long", 500),
	}
	users := make([]*user, len(names))
	for i, name := range names {
		users[i] = s.connect(t, public, name)
	}
	s.awaitSubscribed(t, users...)

	for i, name := range names {
		s.mustPublish(t, public, name, message(map[string]any{"i": i}))
	}
	for i, u := range users {
		if got := decode[struct{ I int }](t, u.next(t, deliveryTimeout)); got.I != i {
			t.Errorf("channel %.40q received message for channel %d", names[i], got.I)
		}
		u.expectNothing(t, 50*time.Millisecond)
	}
}

func TestSameUserManyTabs(t *testing.T) {
	s := startServer(t)
	token := s.userJWT(t, "same-user")
	var tabs []*user
	for range 5 {
		conn, _, err := s.dial(private, "inbox", token, nil)
		if err != nil {
			t.Fatal(err)
		}
		u := &user{scope: private, channel: "inbox", conn: conn, notify: make(chan struct{}, 1), done: make(chan struct{})}
		go u.read()
		t.Cleanup(func() { _ = conn.Close() })
		tabs = append(tabs, u)
	}
	s.awaitSubscribed(t, tabs...)
	s.mustPublish(t, private, "inbox", []byte(`{"n":1}`))
	for _, tab := range tabs {
		if got := string(tab.next(t, deliveryTimeout)); got != `{"n":1}` {
			t.Errorf("tab got %s", got)
		}
	}
}

func TestReconnectReceivesOnlyNewMessages(t *testing.T) {
	s := startServer(t)
	first := s.connect(t, private, "feed")
	s.awaitSubscribed(t, first)
	s.mustPublish(t, private, "feed", []byte(`{"n":1}`))
	first.next(t, deliveryTimeout)
	_ = first.conn.Close()

	s.mustPublish(t, private, "feed", []byte(`{"n":2,"missed":true}`))

	second := s.connect(t, private, "feed")
	s.awaitSubscribed(t, second)
	s.mustPublish(t, private, "feed", []byte(`{"n":3}`))
	if got := string(second.next(t, deliveryTimeout)); got != `{"n":3}` {
		t.Fatalf("reconnected user got %s; the service must not replay history", got)
	}
}

func TestPublishWithoutSubscribersIsAccepted(t *testing.T) {
	s := startServer(t)
	for _, sc := range []scope{private, public} {
		if status := s.publish(t, sc, "empty", []byte(`{}`)); status != http.StatusAccepted {
			t.Errorf("%s: status = %d", sc, status)
		}
	}
}

func TestClientMessagesAreNeverRelayed(t *testing.T) {
	s := startServer(t)
	chatty := s.connect(t, public, "room")
	listener := s.connect(t, public, "room")
	s.awaitSubscribed(t, chatty, listener)

	for i := range 50 {
		if err := chatty.conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{"spoof":%d}`, i))); err != nil {
			t.Fatal(err)
		}
	}
	_ = chatty.conn.WriteMessage(websocket.BinaryMessage, []byte{0xde, 0xad})
	time.Sleep(200 * time.Millisecond)
	s.mustPublish(t, public, "room", []byte(`{"from":"backend"}`))

	for _, u := range []*user{chatty, listener} {
		if got := string(u.next(t, deliveryTimeout)); got != `{"from":"backend"}` {
			t.Errorf("first message = %s; client data was relayed", got)
		}
	}
}

func TestConnectionOutlivesHTTPTimeouts(t *testing.T) {
	if testing.Short() {
		t.Skip("waits past the HTTP server timeouts")
	}
	s := startServer(t)
	u := s.connect(t, private, "long-lived")
	s.awaitSubscribed(t, u)

	time.Sleep(17 * time.Second)
	s.mustPublish(t, private, "long-lived", []byte(`{"still":"connected"}`))
	if got := string(u.next(t, deliveryTimeout)); got != `{"still":"connected"}` {
		t.Fatalf("got %s", got)
	}
}

func TestGracefulShutdownClosesClients(t *testing.T) {
	s := startServer(t)
	users := append(s.connectMany(t, private, "a", 10), s.connectMany(t, public, "b", 10)...)
	s.awaitSubscribed(t, users...)

	start := time.Now()
	if err := s.stop(15 * time.Second); err != nil {
		t.Fatalf("exit: %v\n%s", err, s.logs)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("shutdown took %s", elapsed)
	}
	for _, u := range users {
		err := u.waitClosed(t, 5*time.Second)
		if !websocket.IsCloseError(err, websocket.CloseGoingAway) {
			t.Errorf("close error = %v, want 1001 going away", err)
		}
	}
	if conn, err := net.DialTimeout("tcp", s.addr, time.Second); err == nil {
		_ = conn.Close()
		t.Error("port still accepting connections after shutdown")
	}
	if !strings.Contains(s.logs.String(), "shutting down") {
		t.Error("shutdown not logged")
	}
}

func TestStartupRejectsInvalidConfiguration(t *testing.T) {
	tests := map[string][]option{
		"missing JWT secret":   {withoutEnv("JWT_SECRET")},
		"missing token secret": {withoutEnv("TIME_TOKEN_SECRET")},
		"zero window":          {withEnv("TIME_WINDOW_SECONDS", "0")},
		"invalid skew":         {withEnv("ALLOWED_CLOCK_SKEW", "abc")},
		"invalid body limit":   {withEnv("MAX_BODY_BYTES", "-1")},
		"invalid port":         {withEnv("PORT", "not-a-port")},
	}
	for name, opts := range tests {
		t.Run(name, func(t *testing.T) {
			s := launch(t, raceBinary, opts...)
			select {
			case <-s.exited:
			case <-time.After(10 * time.Second):
				_ = s.cmd.Process.Kill()
				t.Fatal("server started with invalid configuration")
			}
			var exitErr *exec.ExitError
			if !errors.As(s.exitErr, &exitErr) || exitErr.ExitCode() != 1 {
				t.Fatalf("exit = %v, want status 1", s.exitErr)
			}
			if !strings.Contains(s.logs.String(), "server stopped") {
				t.Errorf("no error logged:\n%s", s.logs)
			}
		})
	}
}

func TestStartupFailsWhenPortIsTaken(t *testing.T) {
	if image != "" {
		t.Skip("the container port is always free")
	}
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	port := fmt.Sprint(l.Addr().(*net.TCPAddr).Port)

	s := launch(t, raceBinary, withEnv("PORT", port))
	select {
	case <-s.exited:
	case <-time.After(10 * time.Second):
		_ = s.cmd.Process.Kill()
		t.Fatal("server kept running with its port taken")
	}
	if s.exitErr == nil || !strings.Contains(s.logs.String(), "address already in use") {
		t.Errorf("exit = %v logs:\n%s", s.exitErr, s.logs)
	}
}

func TestUserChannelsDeliverOnlyToTheirOwner(t *testing.T) {
	s := startServer(t)
	aliceTabs := []*user{
		s.connectAs(t, "alice", "user:alice"),
		s.connectAs(t, "alice", "user:alice"),
		s.connectAs(t, "alice", "user:alice"),
	}
	bob := s.connectAs(t, "bob", "user:bob")
	aliceShared := s.connectAs(t, "alice", "orders")
	bobShared := s.connectAs(t, "bob", "orders")
	everyone := append([]*user{bob, aliceShared, bobShared}, aliceTabs...)
	s.awaitSubscribed(t, everyone...)

	s.mustPublish(t, private, "user:alice", []byte(`{"for":"alice"}`))
	for _, tab := range aliceTabs {
		if got := string(tab.next(t, deliveryTimeout)); got != `{"for":"alice"}` {
			t.Errorf("alice tab got %s", got)
		}
	}
	s.mustPublish(t, private, "user:bob", []byte(`{"for":"bob"}`))
	if got := string(bob.next(t, deliveryTimeout)); got != `{"for":"bob"}` {
		t.Errorf("bob got %s", got)
	}
	s.mustPublish(t, private, "orders", []byte(`{"for":"everyone"}`))
	for _, u := range []*user{aliceShared, bobShared} {
		if got := string(u.next(t, deliveryTimeout)); got != `{"for":"everyone"}` {
			t.Errorf("shared subscriber got %s", got)
		}
	}

	time.Sleep(200 * time.Millisecond)
	for _, u := range everyone {
		u.expectNothing(t, 0)
	}
}

func TestUserIDsAreOpaque(t *testing.T) {
	s := startServer(t)
	ids := []string{
		"a",
		"a:b",
		"a:b:c",
		"auth0|5f7c8ec7c33c6c004bbafe82",
		"google-oauth2|103547991597142817347",
		"user@example.com",
		"550e8400-e29b-41d4-a716-446655440000",
		"with space",
		"日本語",
		"emoji-🚀",
		"amp&ersand=1",
		"user:nested",
		"Alice",
		"alice",
		strings.Repeat("long", 200),
	}
	users := make([]*user, len(ids))
	for i, id := range ids {
		users[i] = s.connectAs(t, id, "user:"+id)
	}
	s.awaitSubscribed(t, users...)

	for i, id := range ids {
		s.mustPublish(t, private, "user:"+id, message(map[string]any{"i": i}))
	}
	for i, u := range users {
		if got := decode[struct{ I int }](t, u.next(t, deliveryTimeout)); got.I != i {
			t.Errorf("user %.40q received the message for user %.40q", ids[i], ids[got.I])
		}
		u.expectNothing(t, 50*time.Millisecond)
	}
}
