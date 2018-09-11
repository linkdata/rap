// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build race

package rap

func init() {
	// race detector can only handle max of 8192 goroutines.
	// having too many running at a time won't improve testing,
	// but it will dump a lot of irrelevant goroutine data on
	// panics and timeouts.
	MaxExchangeID = ExchangeID(1024)
}
