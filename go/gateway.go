package rap

// Gateway maintains one or more Streams to the upstream server.
// A Gateway receives incoming HTTP requests and multiplexes these
// onto one or more Streams. It may create new Streams as needed.
type Gateway struct {
	Client *Client
}
