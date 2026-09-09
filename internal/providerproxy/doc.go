// Package providerproxy exposes one controller-owned provider transport through
// an invocation-bound IPv4 loopback endpoint. It never exposes upstream
// credentials, selects providers, retries calls, or treats HTTP delivery as
// proof that the model host received a response.
package providerproxy
