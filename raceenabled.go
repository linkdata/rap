// Copyright 2018 Johan Lindh. All rights reserved.
// Use of this source code is governed by the MIT license, see the LICENSE file.

// +build race

package rap

func init() {
	// race detector can only handle max of 8192 goroutines.
	// having too many running at a time won't improve testing,
	// but it will dump a lot of irrelevant goroutine data on
	// panics and timeouts.
	MaxConnID = ConnID(1024)
}
