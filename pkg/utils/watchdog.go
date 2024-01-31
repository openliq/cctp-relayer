package utils

import (
	"context"
	"fmt"
	"time"
)

var (
	set = make(map[string]int64)
)

func AddProgress(cId string) {
	lock.Lock()
	set[cId] = time.Now().Unix()
	lock.Unlock()
}

func init() {
	go func() {
		for {
			time.Sleep(time.Minute)
			lock.RLock()
			for cId, past := range set {
				if time.Now().Unix()-past >= 300 { // five minute no change，alarm
					Alarm(context.Background(), fmt.Sprintf("cId(%s) work progress not change in five minute", cId))
				}
			}
			lock.RUnlock()
		}
	}()
}
