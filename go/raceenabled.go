// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build race

package rap

func init() {
	raceEnabled = true
	// race detector can only handle max of 8192 goroutines,
	// and we'll need to run two Conn's at the same time.
	MaxExchangeID = ExchangeID(3072)
}
