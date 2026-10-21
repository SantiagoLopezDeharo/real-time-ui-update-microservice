// Package access decides which channels a subscriber may join.
package access

import "strings"

// UserPrefix marks a channel owned by one user: "user:<id>" only accepts
// subscribers whose JWT subject is exactly <id>.
const UserPrefix = "user:"

func IsUserChannel(channel string) bool {
	return strings.HasPrefix(channel, UserPrefix)
}

// CanSubscribe reports whether userID may join channel. Channels without the
// user prefix are shared by every client of the scope. An empty userID is an
// anonymous client, which never owns a user channel.
func CanSubscribe(channel, userID string) bool {
	owner, isUserChannel := strings.CutPrefix(channel, UserPrefix)
	return !isUserChannel || (userID != "" && owner == userID)
}
