package resilience

import (
	"sync"
	"time"
)

var breakers sync.Map

func Get(id string) *Breaker {
	if v, ok := breakers.Load(id); ok {
		return v.(*Breaker)
	}
	b := NewBreaker(5, 30*time.Second)
	actual, _ := breakers.LoadOrStore(id, b)
	return actual.(*Breaker)
}
