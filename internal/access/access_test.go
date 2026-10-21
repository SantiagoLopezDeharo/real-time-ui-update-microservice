package access

import "testing"

func TestIsUserChannel(t *testing.T) {
	tests := map[string]bool{
		"user:42":      true,
		"user:":        true,
		"user:a:b":     true,
		"user:日本":      true,
		"user":         false,
		"users:42":     false,
		"User:42":      false,
		"USER:42":      false,
		" user:42":     false,
		"xuser:42":     false,
		"user%3A42":    false,
		"default":      false,
		"":             false,
		"orders/user:": false,
	}
	for channel, want := range tests {
		if got := IsUserChannel(channel); got != want {
			t.Errorf("IsUserChannel(%q) = %v, want %v", channel, got, want)
		}
	}
}

func TestCanSubscribe(t *testing.T) {
	tests := []struct {
		name    string
		channel string
		userID  string
		want    bool
	}{
		{"shared channel, authenticated", "orders", "42", true},
		{"shared channel, anonymous", "orders", "", true},
		{"default channel", "default", "42", true},
		{"channel resembling the prefix", "users:42", "7", true},
		{"prefix is case sensitive", "User:42", "7", true},
		{"own channel", "user:42", "42", true},
		{"own channel with colons in the id", "user:auth0|a:b", "auth0|a:b", true},
		{"own channel with unicode id", "user:日本", "日本", true},
		{"other user's channel", "user:42", "7", false},
		{"anonymous on user channel", "user:42", "", false},
		{"empty owner, anonymous", "user:", "", false},
		{"empty owner, authenticated", "user:", "42", false},
		{"id prefix of owner", "user:420", "42", false},
		{"owner prefix of id", "user:42", "420", false},
		{"sub-channel of another id", "user:a:b", "a", false},
		{"parent of another id", "user:a", "a:b", false},
		{"case differs", "user:Alice", "alice", false},
		{"leading space", "user: 42", "42", false},
		{"trailing space", "user:42 ", "42", false},
		{"id carries the prefix", "user:user:42", "42", false},
		{"id equals whole channel", "user:42", "user:42", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CanSubscribe(tt.channel, tt.userID); got != tt.want {
				t.Errorf("CanSubscribe(%q, %q) = %v, want %v", tt.channel, tt.userID, got, tt.want)
			}
		})
	}
}

func FuzzCanSubscribe(f *testing.F) {
	f.Add("user:42", "42")
	f.Add("user:a:b", "a")
	f.Add("orders", "")
	f.Fuzz(func(t *testing.T, channel, userID string) {
		got := CanSubscribe(channel, userID)
		if !IsUserChannel(channel) {
			if !got {
				t.Fatalf("shared channel %q denied to %q", channel, userID)
			}
			return
		}
		if want := userID != "" && channel == UserPrefix+userID; got != want {
			t.Fatalf("CanSubscribe(%q, %q) = %v, want %v", channel, userID, got, want)
		}
	})
}
