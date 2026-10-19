package gpool

import (
	"fmt"
	"time"
)

// reservationTTL frees a reservation whose machine creation never finished.
const reservationTTL = 10 * time.Minute

type reservation struct {
	pool, host string
	count      int
	until      time.Time
}

// now is the clock of the reservations; tests replace it.
var now = time.Now

// Reserve holds count GPUs of pool on host for machine uuid while it is created, so two
// machines created at once do not both count the same free GPU. A machine holds one
// reservation; reserving again replaces it.
func (s *Store) Reserve(uuid, pool, host string, count int) error {
	if count <= 0 {
		return fmt.Errorf("%w: a reservation is for one GPU or more", ErrInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.pools[pool]; !ok {
		return fmt.Errorf("%w: no GPU pool %s", ErrNotFound, pool)
	}
	if s.reserved == nil {
		s.reserved = map[string]reservation{}
	}
	s.reserved[uuid] = reservation{pool: pool, host: host, count: count, until: now().Add(reservationTTL)}
	return nil
}

// Release drops machine uuid's reservation (it was created, or failed).
func (s *Store) Release(uuid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.reserved, uuid)
}

// Reserved is how many GPUs of pool on host are promised to machines being created.
func (s *Store) Reserved(pool, host string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, t := 0, now()
	for id, r := range s.reserved {
		if t.After(r.until) {
			delete(s.reserved, id)
			continue
		}
		if r.pool == pool && r.host == host {
			n += r.count
		}
	}
	return n
}
