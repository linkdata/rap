// Copyright 2018 Johan Lindh. All rights reserved.
// Use of this source code is governed by the MIT license, see the LICENSE file.

/*
Package rap implements the REST Aggregation Protocol.

The REST Aggregation Protocol, or RAP for short, is an asymmetric HTTP and WebSocket multiplexing protocol designed to allow high volume of relatively small request-response exchanges while minimizing the resource usage on the upstream server.

One or more RAP gateways are connected to a single upstream server. The gateways receive incoming requests using any protocol it supports (HTTP(S), HTTP/2, SPDY etc) and multiplexes these onto one or more RAP muxers. The gateways need no configuration data except for the upstream destination address.

A RAP muxer multiplexes concurrent requests-response connections (conn), identified by a (small) unsigned integer. The gateway maintains a set of which identifiers are free and may use them in any order. A gateway may open as many muxers as it needs, but should strive to keep as few as possible.

A RAP conn maintains the state of a request-response sequence or WebSocket connection. It also handles the per-conn flow control mechanism, which is a simple transmission window with ACKs from the receiver. Conns inject frames into the muxer for transmission.

A RAP frame is the basic structure within a muxer data stream. It consists of a frame header followed by the frame body data bytes. */
package rap
