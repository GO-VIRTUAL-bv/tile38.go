// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package endpoint builds the target URLs a Tile38 SETHOOK delivers to.
//
// Tile38 parses thirteen endpoint schemes, each with its own path shape and its
// own set of query parameters, and reports a mistake only as an "invalid ..."
// at SETHOOK time — or, worse, as a hook that registers and never delivers.
// Every constructor here returns a plain string, so it drops straight into
// [github.com/GO-VIRTUAL-bv/tile38.go.HookCmd.EndpointURL]:
//
//	c.SetHook("alerts").
//	    EndpointURL(
//	        endpoint.NATS("10.0.0.1:4222", "fleet.events", endpoint.NATSJetstream()),
//	        endpoint.Kafka([]string{"k1:9092", "k2:9092"}, "fleet-events", endpoint.KafkaSSL()),
//	    ).
//	    Within("fleet").Circle(51.05, 3.72, 500).Do(ctx)
//
// The parts Tile38 requires are positional parameters, so they cannot be
// omitted; everything optional is an option func named after the query
// parameter it sets. Path segments and option values are escaped, which
// hand-written URLs routinely are not — a password containing "&" ends the
// query string early, and a NATS subject containing "/" is silently truncated
// to its first segment.
//
// Nothing here validates or returns an error: the server's own parser is the
// only authority on what it accepts, and a constructor that returned
// (string, error) could not be nested inside EndpointURL. What the constructors
// do instead is make the documented failures unreachable — see [NATS] for the
// port rule and [MQTTRetained] for the one option Tile38 will not read as a
// boolean.
//
// http:// and https:// endpoints need no helper: Tile38 takes those URLs
// verbatim, so pass them to EndpointURL as they are. Two caveats. Tile38 reads
// any https URL beginning "https://sqs." and containing ".amazonaws.com" as a
// plain-URL SQS endpoint rather than an HTTP one. And SETCHAN needs nothing
// from this package at all — a channel is always local://<name>, which the
// server fills in itself.
package endpoint

import (
	"net/url"
	"strconv"
	"strings"
)

// build renders "scheme://host[/seg…][?query]". Every path segment and every
// query value is escaped with url.QueryEscape, which is what Tile38's parser
// reverses: it reads path segments back through url.QueryUnescape and the query
// through url.ParseQuery.
func build(scheme, host string, q url.Values, segments ...string) string {
	var b strings.Builder
	b.WriteString(scheme)
	b.WriteString("://")
	b.WriteString(host)
	for _, seg := range segments {
		b.WriteByte('/')
		b.WriteString(url.QueryEscape(seg))
	}
	if len(q) > 0 {
		b.WriteByte('?')
		b.WriteString(q.Encode())
	}
	return b.String()
}

// values applies opts to a fresh url.Values. Every option type in this package
// is a func(url.Values), so one helper serves all of them.
func values[Option ~func(url.Values)](opts []Option) url.Values {
	q := url.Values{}
	for _, opt := range opts {
		opt(q)
	}
	return q
}

// ── local ─────────────────────────────────────────────────────────────────────

// Local returns a local://<channel> endpoint, which publishes to a Tile38 pub/sub
// channel that Subscribe clients read. It is what SETCHAN registers for you, and
// is worth naming on a hook only to mix a channel in with other endpoints.
func Local(channel string) string {
	return build("local", channel, nil)
}

// ── grpc ──────────────────────────────────────────────────────────────────────

// GRPC returns a grpc://<host>[:<port>] endpoint. Tile38 defaults the port to 80
// when host carries none.
func GRPC(host string) string {
	return build("grpc", host, nil)
}

// ── redis ─────────────────────────────────────────────────────────────────────

// Redis returns a redis://<host>[:<port>]/<channel> endpoint, publishing each
// event to a Redis pub/sub channel. Tile38 defaults the port to 6379.
func Redis(host, channel string) string {
	return build("redis", host, nil, channel)
}

// ── disque ────────────────────────────────────────────────────────────────────

// DisqueOption sets a query parameter on a Disque endpoint.
type DisqueOption func(url.Values)

// DisqueReplicate sets Disque's replication factor for the queued job.
func DisqueReplicate(n int) DisqueOption {
	return func(q url.Values) { q.Set("replicate", strconv.Itoa(n)) }
}

// Disque returns a disque://<host>[:<port>]/<queue> endpoint. Tile38 defaults
// the port to 7711 and rejects the URL outright without a queue name.
func Disque(host, queue string, opts ...DisqueOption) string {
	return build("disque", host, values(opts), queue)
}

// ── kafka ─────────────────────────────────────────────────────────────────────

// KafkaOption sets a query parameter on a Kafka endpoint.
type KafkaOption func(url.Values)

// KafkaAuth selects the SASL mechanism, matching Tile38's "auth" parameter.
func KafkaAuth(mechanism string) KafkaOption {
	return func(q url.Values) { q.Set("auth", mechanism) }
}

// KafkaSSL connects to the brokers over TLS.
func KafkaSSL() KafkaOption {
	return func(q url.Values) { q.Set("ssl", "true") }
}

// KafkaCACert names a CA certificate file, read by the Tile38 server rather than
// by this process.
func KafkaCACert(path string) KafkaOption {
	return func(q url.Values) { q.Set("cacert", path) }
}

// KafkaCert names a client certificate file, read by the Tile38 server.
func KafkaCert(path string) KafkaOption {
	return func(q url.Values) { q.Set("cert", path) }
}

// KafkaKey names a client key file, read by the Tile38 server.
func KafkaKey(path string) KafkaOption {
	return func(q url.Values) { q.Set("key", path) }
}

// KafkaSASLSHA256 selects SCRAM-SHA-256 for SASL authentication.
func KafkaSASLSHA256() KafkaOption {
	return func(q url.Values) { q.Set("sha256", "true") }
}

// KafkaSASLSHA512 selects SCRAM-SHA-512 for SASL authentication.
func KafkaSASLSHA512() KafkaOption {
	return func(q url.Values) { q.Set("sha512", "true") }
}

// Kafka returns a kafka://<broker>[,<broker>…]/<topic> endpoint. Tile38 defaults
// a broker given without a port to :9092, and rejects the URL with "missing
// kafka topic name" if topic is empty.
//
// The comma-joined broker list survives SETHOOK's own endpoint separator:
// Tile38 splits an endpoint argument on commas but rejoins any part that carries
// no scheme, so several brokers stay one endpoint.
func Kafka(brokers []string, topic string, opts ...KafkaOption) string {
	return build("kafka", strings.Join(brokers, ","), values(opts), topic)
}

// ── amqp ──────────────────────────────────────────────────────────────────────

// AMQPOption sets a query parameter on an AMQP endpoint.
type AMQPOption func(url.Values)

// AMQPRoute sets the routing key. Tile38 defaults it to "tile38".
func AMQPRoute(key string) AMQPOption {
	return func(q url.Values) { q.Set("route", key) }
}

// AMQPType sets the exchange type. Tile38 defaults it to "direct".
func AMQPType(exchangeType string) AMQPOption {
	return func(q url.Values) { q.Set("type", exchangeType) }
}

// AMQPDurable declares the queue durable or not. It takes a value because
// Tile38 defaults this one to true, unlike every other AMQP flag.
func AMQPDurable(durable bool) AMQPOption {
	return func(q url.Values) { q.Set("durable", strconv.FormatBool(durable)) }
}

// AMQPInternal declares the exchange internal.
func AMQPInternal() AMQPOption {
	return func(q url.Values) { q.Set("internal", "true") }
}

// AMQPNoWait declares the queue without waiting for the broker to confirm.
func AMQPNoWait() AMQPOption {
	return func(q url.Values) { q.Set("no_wait", "true") }
}

// AMQPAutoDelete deletes the queue once its last consumer disconnects.
func AMQPAutoDelete() AMQPOption {
	return func(q url.Values) { q.Set("auto_delete", "true") }
}

// AMQPImmediate publishes with the immediate flag.
func AMQPImmediate() AMQPOption {
	return func(q url.Values) { q.Set("immediate", "true") }
}

// AMQPMandatory publishes with the mandatory flag.
func AMQPMandatory() AMQPOption {
	return func(q url.Values) { q.Set("mandatory", "true") }
}

// AMQPDeliveryMode sets the delivery mode: 1 transient, 2 persistent. Tile38
// defaults it to transient.
func AMQPDeliveryMode(mode uint8) AMQPOption {
	return func(q url.Values) { q.Set("delivery_mode", strconv.FormatUint(uint64(mode), 10)) }
}

// AMQPPriority sets the message priority.
func AMQPPriority(priority uint8) AMQPOption {
	return func(q url.Values) { q.Set("priority", strconv.FormatUint(uint64(priority), 10)) }
}

// AMQP returns an amqp://<host>/<queue> endpoint. Tile38 rejects the URL with
// "missing AMQP queue name" if queue is empty.
//
// host is written verbatim and carries whatever the broker needs in front of the
// queue — credentials and port ("guest:guest@localhost:5672"), and a vhost as a
// trailing path segment ("guest:guest@localhost:5672/production"), which Tile38
// folds into the dial URI rather than reading as part of the queue name.
func AMQP(host, queue string, opts ...AMQPOption) string {
	return build("amqp", host, values(opts), queue)
}

// AMQPS is AMQP over TLS. It is a separate constructor because Tile38 reads the
// scheme itself rather than a parameter.
func AMQPS(host, queue string, opts ...AMQPOption) string {
	return build("amqps", host, values(opts), queue)
}

// ── mqtt ──────────────────────────────────────────────────────────────────────

// MQTTOption sets a query parameter on an MQTT endpoint.
type MQTTOption func(url.Values)

// MQTTQoS sets the quality-of-service level: 0, 1 or 2.
func MQTTQoS(level uint8) MQTTOption {
	return func(q url.Values) { q.Set("qos", strconv.FormatUint(uint64(level), 10)) }
}

// MQTTRetained marks published messages retained.
//
// It sends "1" rather than "true": Tile38 parses this one parameter as an
// integer and rejects anything but 0 or 1 with "invalid MQTT retained value",
// where every other flag it reads goes through a boolean parser.
func MQTTRetained() MQTTOption {
	return func(q url.Values) { q.Set("retained", "1") }
}

// MQTTCACert names a CA certificate file, read by the Tile38 server.
func MQTTCACert(path string) MQTTOption {
	return func(q url.Values) { q.Set("cacert", path) }
}

// MQTTCert names a client certificate file, read by the Tile38 server.
func MQTTCert(path string) MQTTOption {
	return func(q url.Values) { q.Set("cert", path) }
}

// MQTTKey names a client key file, read by the Tile38 server.
func MQTTKey(path string) MQTTOption {
	return func(q url.Values) { q.Set("key", path) }
}

// MQTTUser sets the username.
func MQTTUser(user string) MQTTOption {
	return func(q url.Values) { q.Set("user", user) }
}

// MQTTPass sets the password.
func MQTTPass(pass string) MQTTOption {
	return func(q url.Values) { q.Set("pass", pass) }
}

// MQTT returns an mqtt://<host>[:<port>]/<topic> endpoint. Tile38 defaults the
// port to 1883 and rejects the URL with "missing MQTT topic name" if topic is
// empty. A multi-level topic ("fleet/eu/events") is escaped whole and arrives
// intact.
func MQTT(host, topic string, opts ...MQTTOption) string {
	return build("mqtt", host, values(opts), topic)
}

// MQTTS is MQTT over TLS. It is a separate constructor because Tile38 reads the
// scheme itself rather than a parameter.
func MQTTS(host, topic string, opts ...MQTTOption) string {
	return build("mqtts", host, values(opts), topic)
}

// ── sqs ───────────────────────────────────────────────────────────────────────

// SQSOption sets a query parameter on an SQS endpoint.
type SQSOption func(url.Values)

// SQSCredPath names the AWS credentials file, read by the Tile38 server.
func SQSCredPath(path string) SQSOption {
	return func(q url.Values) { q.Set("credpath", path) }
}

// SQSCredProfile selects a profile within the credentials file.
func SQSCredProfile(profile string) SQSOption {
	return func(q url.Values) { q.Set("credprofile", profile) }
}

// SQSCreateQueue has Tile38 create the queue if it does not exist.
func SQSCreateQueue() SQSOption {
	return func(q url.Values) { q.Set("createqueue", "true") }
}

// SQS returns an sqs://<region>:<queueID>/<queueName> endpoint. The region and
// queue ID sit in the host position as a colon-joined pair — this is not a
// host:port — and Tile38 rejects the URL with "invalid SQS url" unless both are
// present.
//
// A ready-made queue URL takes no helper: Tile38 recognises any https URL
// beginning "https://sqs." and containing ".amazonaws.com" as an SQS endpoint,
// so pass one straight to EndpointURL.
func SQS(region, queueID, queueName string, opts ...SQSOption) string {
	return build("sqs", region+":"+queueID, values(opts), queueName)
}

// ── pubsub ────────────────────────────────────────────────────────────────────

// PubSubOption sets a query parameter on a Google Cloud Pub/Sub endpoint.
type PubSubOption func(url.Values)

// PubSubCredPath names the GCP credentials file, read by the Tile38 server.
func PubSubCredPath(path string) PubSubOption {
	return func(q url.Values) { q.Set("credpath", path) }
}

// PubSub returns a pubsub://<project>:<topic> endpoint for Google Cloud Pub/Sub.
// Project and topic are a colon-joined pair in the host position, with no path
// segment; Tile38 rejects anything else with "invalid PubSub format".
func PubSub(project, topic string, opts ...PubSubOption) string {
	return build("pubsub", project+":"+topic, values(opts))
}

// ── nats ──────────────────────────────────────────────────────────────────────

// NATSOption sets a query parameter on a NATS endpoint.
type NATSOption func(url.Values)

// NATSUser sets the username.
func NATSUser(user string) NATSOption {
	return func(q url.Values) { q.Set("user", user) }
}

// NATSPass sets the password.
func NATSPass(pass string) NATSOption {
	return func(q url.Values) { q.Set("pass", pass) }
}

// NATSToken sets a token, the alternative to user and password.
func NATSToken(token string) NATSOption {
	return func(q url.Values) { q.Set("token", token) }
}

// NATSSecure connects over TLS using the system roots.
func NATSSecure() NATSOption {
	return func(q url.Values) { q.Set("secure", "true") }
}

// NATSCredential names a NATS credentials file, read by the Tile38 server.
func NATSCredential(path string) NATSOption {
	return func(q url.Values) { q.Set("credential", path) }
}

// NATSJetstream publishes through JetStream, so the server waits for a publish
// acknowledgement rather than firing and forgetting.
func NATSJetstream() NATSOption {
	return func(q url.Values) { q.Set("jetstream", "true") }
}

// NATSTLS connects over TLS using the certificate and key given by NATSTLSCert
// and NATSTLSKey.
func NATSTLS() NATSOption {
	return func(q url.Values) { q.Set("tls", "true") }
}

// NATSTLSCert names a client certificate file, read by the Tile38 server.
func NATSTLSCert(path string) NATSOption {
	return func(q url.Values) { q.Set("tlscert", path) }
}

// NATSTLSKey names a client key file, read by the Tile38 server.
func NATSTLSKey(path string) NATSOption {
	return func(q url.Values) { q.Set("tlskey", path) }
}

// NATS returns a nats://<host>:<port>/<subject> endpoint.
//
// The port is not optional to Tile38 — its parser accepts only a host:port pair
// here, and rejects "nats://host/subject" with the message "invalid SQS url",
// an upstream copy/paste that makes the failure very hard to read. A host given
// without one therefore gets NATS's default 4222 rather than that error.
//
// The subject is escaped, so a dotted subject arrives intact. Tile38 reads only
// the first path segment, so a subject written with slashes would otherwise be
// silently truncated.
func NATS(host, subject string, opts ...NATSOption) string {
	if !strings.Contains(host, ":") {
		host += ":4222"
	}
	return build("nats", host, values(opts), subject)
}

// ── eventhub ──────────────────────────────────────────────────────────────────

// EventHub returns an Azure Event Hubs connection string, the one endpoint that
// is not a URL. Tile38 recognises it by its "Endpoint=" prefix and requires
// exactly these four semicolon-separated parts in this order, so the pieces are
// all positional:
//
//	Endpoint=<uri>;SharedAccessKeyName=<n>;SharedAccessKey=<k>;EntityPath=<p>
//
// uri is the namespace endpoint Azure lists for the hub, e.g.
// "sb://example.servicebus.windows.net/".
func EventHub(uri, keyName, key, entityPath string) string {
	return "Endpoint=" + uri +
		";SharedAccessKeyName=" + keyName +
		";SharedAccessKey=" + key +
		";EntityPath=" + entityPath
}

// ── cf-queue ──────────────────────────────────────────────────────────────────

// CFQueue returns a cf-queue://<accountID>/<queueID>?token=<apiToken> endpoint
// for Cloudflare Queues. All three parts are positional because Tile38 rejects
// the URL if any of them is missing — the API token included, which is the only
// query parameter it treats as required.
func CFQueue(accountID, queueID, apiToken string) string {
	q := url.Values{}
	q.Set("token", apiToken)
	return build("cf-queue", accountID, q, queueID)
}
