package bot

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// pending is an action waiting for a Yes/No tap.
type pending struct {
	UserID    int64
	ChatID    int64
	Container string
	Action    string
	Expires   time.Time
}

// confirmStore keeps pending confirmations in memory. Entries are single-use,
// expire after ttl and can only be redeemed by the user and chat that created them.
type confirmStore struct {
	mu    sync.Mutex
	ttl   time.Duration
	now   func() time.Time
	items map[string]pending
}

func newConfirmStore(ttl time.Duration, now func() time.Time) *confirmStore {
	return &confirmStore{ttl: ttl, now: now, items: map[string]pending{}}
}

func (s *confirmStore) add(userID, chatID int64, container, action string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for k, p := range s.items { // drop expired entries
		if now.After(p.Expires) {
			delete(s.items, k)
		}
	}
	var raw [8]byte
	_, _ = rand.Read(raw[:])
	id := hex.EncodeToString(raw[:])
	s.items[id] = pending{UserID: userID, ChatID: chatID, Container: container, Action: action, Expires: now.Add(s.ttl)}
	return id
}

type takeResult int

const (
	takeOK takeResult = iota
	takeUnknown
	takeExpired
	takeForeign
)

// take redeems (and removes) a confirmation. A foreign user or chat leaves the
// entry in place so the rightful owner can still use it.
func (s *confirmStore) take(id string, userID, chatID int64) (pending, takeResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.items[id]
	if !ok {
		return pending{}, takeUnknown
	}
	if p.UserID != userID || p.ChatID != chatID {
		return pending{}, takeForeign
	}
	delete(s.items, id)
	if s.now().After(p.Expires) {
		return pending{}, takeExpired
	}
	return p, takeOK
}

// cancel removes a confirmation owned by the given user and chat.
func (s *confirmStore) cancel(id string, userID, chatID int64) bool {
	_, r := s.take(id, userID, chatID)
	return r == takeOK || r == takeExpired
}
