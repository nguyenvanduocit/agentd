package pairingtest

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/nguyenvanduocit/agentd/sdk/goserver"
)

// NewJSONStore is a goserver.PairingStore whose whole state is one piece of JSON text, decoded and encoded again by
// every call, as a store in front of a database would: nothing is shared with a handle but the text. It is a second
// implementation of the contract, built differently from the one that ships with goserver, for the suite to run against
// and for an app's own tests to use in place of Redis. It is not for production: one lock, one blob. Since 0.8.0.
func NewJSONStore() goserver.PairingStore {
	a, _ := NewJSONStorePair()
	return a
}

// NewJSONStorePair is two handles over one JSON store: two instances of an app that share a pairing store. Since 0.8.0.
func NewJSONStorePair() (a, b goserver.PairingStore) {
	backend := &jsonBackend{}
	return &jsonHandle{backend}, &jsonHandle{backend}
}

type jsonBackend struct {
	mu   sync.Mutex
	text []byte
}

type jsonState struct {
	Users    map[string]jsonCode    `json:"users"`
	Codes    map[string]string      `json:"codes"`
	Failures map[string][]time.Time `json:"failures"`
}

type jsonCode struct {
	Code    string    `json:"code"`
	Expires time.Time `json:"expires"`
}

type jsonHandle struct{ b *jsonBackend }

// do decodes the state, lets f change it, and writes it back when f says it changed.
func (h *jsonHandle) do(f func(*jsonState) (changed bool, err error)) error {
	h.b.mu.Lock()
	defer h.b.mu.Unlock()
	var st jsonState
	if len(h.b.text) > 0 {
		if err := json.Unmarshal(h.b.text, &st); err != nil {
			return err
		}
	}
	if st.Users == nil {
		st.Users = map[string]jsonCode{}
	}
	if st.Codes == nil {
		st.Codes = map[string]string{}
	}
	if st.Failures == nil {
		st.Failures = map[string][]time.Time{}
	}
	changed, err := f(&st)
	if changed {
		text, merr := json.Marshal(st)
		if merr != nil {
			return merr
		}
		h.b.text = text
	}
	return err
}

func (h *jsonHandle) Issue(_ context.Context, userID, code string, now, expires time.Time) error {
	return h.do(func(st *jsonState) (bool, error) {
		if owner, taken := st.Codes[code]; taken && owner != userID {
			if now.Before(st.Users[owner].Expires) {
				return false, goserver.ErrCodeTaken
			}
			delete(st.Users, owner)
			delete(st.Codes, code)
		}
		if old, ok := st.Users[userID]; ok {
			delete(st.Codes, old.Code)
		}
		st.Users[userID] = jsonCode{Code: code, Expires: expires}
		st.Codes[code] = userID
		return true, nil
	})
}

func (h *jsonHandle) Cancel(_ context.Context, userID string) error {
	return h.do(func(st *jsonState) (bool, error) {
		old, ok := st.Users[userID]
		if !ok {
			return false, nil
		}
		delete(st.Codes, old.Code)
		delete(st.Users, userID)
		return true, nil
	})
}

func (h *jsonHandle) Pending(_ context.Context, userID string, now time.Time) (code string, expires time.Time, err error) {
	err = h.do(func(st *jsonState) (bool, error) {
		if p, ok := st.Users[userID]; ok && now.Before(p.Expires) {
			code, expires = p.Code, p.Expires
		}
		return false, nil
	})
	return code, expires, err
}

func (h *jsonHandle) Redeem(_ context.Context, code string, now time.Time) (userID string, err error) {
	err = h.do(func(st *jsonState) (bool, error) {
		owner, ok := st.Codes[code]
		if !ok {
			return false, goserver.ErrCodeNotFound
		}
		live := now.Before(st.Users[owner].Expires)
		delete(st.Users, owner)
		delete(st.Codes, code)
		if !live {
			return true, goserver.ErrCodeNotFound
		}
		userID = owner
		return true, nil
	})
	return userID, err
}

// within are the failures that are younger than window at now.
func within(failures []time.Time, now time.Time, window time.Duration) []time.Time {
	var keep []time.Time
	for _, f := range failures {
		if now.Sub(f) < window {
			keep = append(keep, f)
		}
	}
	return keep
}

func (h *jsonHandle) Failures(_ context.Context, key string, now time.Time, window time.Duration) (n int, err error) {
	err = h.do(func(st *jsonState) (bool, error) {
		n = len(within(st.Failures[key], now, window))
		return false, nil
	})
	return n, err
}

func (h *jsonHandle) Fail(_ context.Context, key string, now time.Time, window time.Duration) (n int, err error) {
	err = h.do(func(st *jsonState) (bool, error) {
		recent := append(within(st.Failures[key], now, window), now)
		st.Failures[key] = recent
		n = len(recent)
		return true, nil
	})
	return n, err
}
