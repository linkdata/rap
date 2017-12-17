[![Build Status](https://travis-ci.org/linkdata/rap.svg?branch=master)](https://travis-ci.org/linkdata/rap) [![Coverage Status](https://img.shields.io/coveralls/linkdata/rap.svg)](https://coveralls.io/github/linkdata/rap?branch=master)

# REST Aggregation Protocol

The REST Aggregation Protocol, or *RAP* for short, is an asymmetric HTTP and WebSocket reverse-proxy multiplexing protocol designed to allow high volume of relatively small request-response exchanges while minimizing the resource usage on the upstream server.

## Motivation

Parsing HTTP and correctly implementing all the features of a HTTP server uses up significant resources on the server. Simply handling the TCP protocol requirements for tens of thousands of connections can consume significant CPU resources on the server.

Traditional web applications simply accept this as an unavoidable fact and focus on finding ways to add more servers. But that brings it's own set of problems and isn't a viable solution for a CPU-bound server where scaling out isn't an option.

In some architectures it may not be possible to decouple the application code that processes HTTP requests from the database that holds the data. So everything that runs on the same machine must be as efficient as possble. Unfortunately, even the most efficient web servers today use far too much CPU.

This project aims to move the web server to other machine(s) and to simplify the request-response scheme to support only what we need while making sure that receiving and routing the incoming requests use as few resources as possible. Where HTTP tries to be very generic in it's design, RAP focuses on handling large amounts of small request-reply exchanges.

I looked to HTTP/2 as a likely candidate for multiplexing HTTP requests, but found that it used too much CPU and that the HPACK algorithm made the stream stateful, which meant synchronization mechanisms would be needed in order to use more than one thread per stream on the upstream server.

## Overview

One or more RAP *gateways* are connected to a single upstream server. The gateways receive incoming requests using any protocol it supports (HTTP(S), HTTP/2, SPDY etc) and multiplexes these onto one or more RAP *connections*. The gateways need no configuration data except for the upstream destination address.

A RAP *connection* multiplexes concurrent requests-response *exchanges*, identified by a (small) unsigned integer. The gateway maintains a set of which exchange identifiers are free and may use them in any order. A gateway may open as many connections as it needs, but should strive to keep as few as possible.

A RAP *exchange* maintains the state of a request-response sequence or WebSocket connection. It also handles the per-exchange flow control mechanism, which is a simple transmission window with ACKs from the receiver. Exchanges inject *frames* into the connection for transmission.

A RAP *frame* is the basic structure within a stream. It consists of a *frame header* followed by the *frame body* data bytes.

A RAP *frame header* is 32 bits, divided into a 16-bit Size value, a 3-bit control field and a 13-bit exchange Index. If Index is 0x1fff (highest possible), the frame is a stream control frame and the control field is a 3-bit MSB value specifying the frame type:
* 000 - reserved, may not have payload
* 001 - reserved, but expect Size to reflect payload size
* 010 - Ping, Size is bytes of payload data to return in a Pong
* 011 - Pong, Size is bytes of payload data as received in the Ping
* 100 - reserved, may not have payload
* 101 - reserved, expect Size to reflect payload size
* 110 - reserved, expect Size to reflect payload size
* 111 - Panic, sender is shutting down due to error, Size is bytes of optional technical information

If Index is 0..0x1ffe (inclusive), the frame applies to that exchange, and the control field is mapped to three flags: Final, Head and Body. If neither Head nor Body flags are set, the frame is a flow control frame and the Size is ignored.
* 001 - Body - if set, data bytes form body data (after any RAP record, if present)
* 010 - Head - if set, the data bytes starts with a RAP *record*
* 100 - Final - if set, this is the final frame for the exchange

## RAP records

A RAP *record* type defines how the data bytes are encoded. The records have fields that are encoded using *RAP data types* such as *string* or *length*. Their definitions can be found in the section *RAP data types*.

### Invalid record (0x00)

Never a valid record to send. If received, the connection is terminated immediately.

### Set string record (0x01)

Set one or more string lookups for a connection. Once set, a string lookup value must not be changed. Note that each side maintains both it's own lookup table and the peer's lookup table. Receiving this record adds to the table used when sending strings to the peer. When receiving strings, each side must be able to resolve lookups that has previously been sent.
* One or more of:
  * `length` Lookup index. Must be a value greater than 1. Must not previously have been set.
  * `string` Lookup string. Must not be a zero-length string.
* `0x00` Terminator. Signals the end of the table.

### Set route record (0x02)

Set one or more route lookups for a connection. Once set, a route lookup value must not be changed. Only the upstream server may send this message to a connected gateway.
* One or more of:
  * `length` Lookup index. Must be a value greater than zero. Must not previously have been set.
  * `string` Host name. If the empty string (`0x00 0x01`), route applies to all host names.
  * `string` Route definition string. Must be a legal [naoina/denco URL pattern](https://github.com/naoina/denco#url-patterns).
* `0x00` Terminator. Signals the end of the table.

### HTTP request record (0x03)

Sent from the gateway to start a new HTTP exchange. The record structure contains enough information to transparently carry a HTTP/1.1 request. Since the gateway must validate incoming requests and format them into request records, the upstream server receiving them may rely on the structure being correct.
* `string` HTTP method, e.g. `GET`.
* `route` The route information or URI path.
* `kvv` URI query component. Both keys and values must be URI-encoded. An empty `kvv` implies no query portion was present. This means the protocol cannot distinguish `/some/path` from `/some/path?`.
* `kvv` HTTP request headers. Keys must be in `Canonical-Format`. Values must comply with RFC 2616 section 4.2. Note that the `Host` and `Content-Length` headers are provided separately at the end, and must not appear here.
* `string` HTTP `Host` header value.
* `int64` HTTP `Content-Length` header value. If `-1`, then `Content-Length` header is not present.

### HTTP response record (0x04)

Sent from the upstream server in response to a HTTP request record.
* `length` HTTP status code. Must be in the range 100-599, inclusive.
* `kvv` HTTP response headers. Keys must be in `Canonical-Format`. Values must comply with RFC 2616 section 4.2. The gateway must supply any required headers that are omitted, so that upstream need not send `Date` or `Server`.
* `int64` HTTP `Content-Length` header value. If the `Content-Length` HTTP header is not present, and this value is not negative, the gateway will insert a `Content-Length` header with the value given.

### Service stopping record (0x05)

Sent from the upstream server to signal that no new requests may be initiated. New requests that cannot be served from cache will have the status code and reason provided. If a record body is provided, it should be provided as the response body. Note that this record applies to all connections from the client until a *service resume* record is received.
This record has the same definition as the *HTTP response record*, and is parsed the same.

### Service resume record (0x06)

Sent to resume service again after a *service stopping* record.

### User defined record (0x80)

Marks the first user record value available for the application using the RAP protocol. Unhandled user records are discarded silently. The last user defined record value is `0xFF`.

## RAP data types

### `uint64`

MSB encoded 7 bits at a time, with the high bit functioning as a stop bit. For each byte, the lower 7 bits are appended to the result. If the high bit is set, keep going. If the high bit is clear, return the result.

### `int64`

If the value is zero or positive, shift the value left one bit and encode as a `uint64`.
If the value is negative, shift the absolute value left one bit and set the lowest bit to one, then encode as a `uint64`.

### `length`

Used to encode non-negative small integers primarily for string lengths.
A negative value or a value greater than 32767 cannot be encoded and is an error.
If the value is less than 128, write it as a single byte.
Otherwise, encode it using two bytes in MSB order, with the first byte having the high bit set.

Examples:
* `0x0000` -> `0x00`
* `0x0001` -> `0x01`
* `0x007F` -> `0x7F`
* `0x0080` -> `0x80` `0x80`
* `0x1234` -> `0x92` `0x34`
* `0x7FFF` -> `0xFF` `0xFF`
* `0x8000` or more -> *error*

### `string`

A string encoding starts with a `length`. If the length is nonzero, then that many binary bytes follow.
A zero length signals special case handling and is followed by another `length` value, interpreted as follows:
* `0x00` Null string. Used to mark the end of a list of strings or signal other special cases.
* `0x01` Empty string, i.e. `""`.
* Other values refer to entries in the connection string lookup table for received strings. If an undefined string lookup value is seen, it is a fatal error and the stream must be closed.

### `kvv`

A key-value-value structure used to encode request query parameters and request-response headers.
* Zero or more of:
  * `string` Key. 
  * Each key has zero or more values:
    * `string` Value. Unordered value associated with the key.
  * `string` Null string (`0x00 0x00`) marking the end of values for the key.
* `string` Null string (`0x00 0x00`) marking the end of keys for this `kvv` set.

### `route`

Either:
* `length` Index of a registered route. Must be greater than zero.
* Zero or more of:
  * `string` the parameter values associated with the route, in the order they appeared in the route string when it was registered.

Or:
* `0x00` A `length` value of zero.
* `string` A non-zero length string with the URI path component using `/` as a separator. It must be URI-decoded (no `%xx`). Must be absolute (start with a `/`) and normalized, meaning it must not contain any `.` or `..` elements.

## Flow control

Each side of an *exchange* maintains a count of non-final data *frames* sent but not acknowledged. Frames sent with the exchange id set to `0x1FFF`, those with the final bit set, or those without payload are not counted.

A receiver must be able to buffer the full window size count of *frames* per *exchange*. When a received *frame* that is counted is processed, the receiver must acknowledge receipt of it by sending a *frame header* with the same *exchange id*, control bits set to `000` (not final, no head data, no body data) and the size value set to zero. This is called a *flow control frame*.

Before an *exchange* is done and it's id may be reused, both sides must send and receive a *frame* with the *final* control bit set. This is known as the *final frame*. After the *final frame* is sent, only flow control frames may be sent. Upon receiving a *final frame*, we must either send a *final frame* in response if we haven't already, or release the exchange for reuse. After sending a *final frame*, we must wait for a *final frame* in response if we haven't already got one, and then wait for the flow control window to drain.

## License

OSS license TBD.

The RAP specification is Copyright :copyright: 2015-2017 Johan Lindh

Big thanks to Starcounter AB for transferring the copyright to me and allowing me to open-source this project!
