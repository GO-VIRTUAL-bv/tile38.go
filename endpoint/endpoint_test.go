// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package endpoint

import "testing"

// Pins the shapes and escapes Tile38's parser requires. The live acceptance
// test is TestEndpointURLs in the root package, which feeds every one of these
// to a real SETHOOK; this covers the byte-level decisions a round trip cannot
// show, and runs without Docker.
func TestBuild(t *testing.T) {
	tests := map[string]struct {
		got  string
		want string
	}{
		"local channel sits in the host position": {
			got:  Local("alerts"),
			want: "local://alerts",
		},
		"grpc takes no path": {
			got:  GRPC("10.0.0.1:9000"),
			want: "grpc://10.0.0.1:9000",
		},
		"redis channel is a path segment": {
			got:  Redis("10.0.0.1", "events"),
			want: "redis://10.0.0.1/events",
		},
		"disque replicate": {
			got:  Disque("10.0.0.1:7711", "jobs", DisqueReplicate(2)),
			want: "disque://10.0.0.1:7711/jobs?replicate=2",
		},
		// Tile38 splits an endpoint argument on commas but rejoins any part
		// with no scheme, so a broker list survives as one endpoint.
		"kafka brokers stay comma-joined": {
			got:  Kafka([]string{"k1:9092", "k2"}, "fleet-events", KafkaSSL(), KafkaSASLSHA512()),
			want: "kafka://k1:9092,k2/fleet-events?sha512=true&ssl=true",
		},
		// Durable is the one AMQP flag Tile38 defaults to true, so it has to be
		// able to send false.
		"amqp durable can be turned off": {
			got:  AMQP("guest:guest@10.0.0.1:5672", "events", AMQPDurable(false), AMQPRoute("fleet")),
			want: "amqp://guest:guest@10.0.0.1:5672/events?durable=false&route=fleet",
		},
		"amqps is its own scheme": {
			got:  AMQPS("10.0.0.1:5671", "events"),
			want: "amqps://10.0.0.1:5671/events",
		},
		// Tile38 parses retained as an integer and rejects "true" outright,
		// where every other flag it reads goes through a boolean parser.
		"mqtt retained is 1, not true": {
			got:  MQTT("10.0.0.1", "fleet", MQTTRetained(), MQTTQoS(2)),
			want: "mqtt://10.0.0.1/fleet?qos=2&retained=1",
		},
		// MQTT is the one scheme that reads every path segment, but escaping the
		// topic whole round-trips through its url.QueryUnescape either way.
		"mqtt multi-level topic is escaped whole": {
			got:  MQTTS("10.0.0.1:8883", "fleet/eu/events"),
			want: "mqtts://10.0.0.1:8883/fleet%2Feu%2Fevents",
		},
		// Region and queue ID are a colon-joined pair in the host position —
		// this is not a host:port.
		"sqs pairs region and queue id": {
			got:  SQS("eu-central-1", "123456789", "fleet", SQSCreateQueue()),
			want: "sqs://eu-central-1:123456789/fleet?createqueue=true",
		},
		"pubsub pairs project and topic with no path": {
			got:  PubSub("acme", "fleet", PubSubCredPath("/etc/gcp.json")),
			want: "pubsub://acme:fleet?credpath=%2Fetc%2Fgcp.json",
		},
		// Tile38 accepts only host:port for NATS, and rejects a bare host with
		// the message "invalid SQS url".
		"nats defaults the port it insists on": {
			got:  NATS("10.0.0.1", "fleet.events"),
			want: "nats://10.0.0.1:4222/fleet.events",
		},
		"nats keeps an explicit port": {
			got:  NATS("10.0.0.1:4333", "fleet.events"),
			want: "nats://10.0.0.1:4333/fleet.events",
		},
		// Tile38 reads only the first path segment, so an unescaped slashed
		// subject would silently become "fleet".
		"nats subject escapes its slashes": {
			got:  NATS("10.0.0.1:4222", "fleet/eu/events"),
			want: "nats://10.0.0.1:4222/fleet%2Feu%2Fevents",
		},
		// An unescaped "&" in a password ends the query string early and takes
		// every later parameter with it.
		"option values are escaped": {
			got:  NATS("10.0.0.1:4222", "fleet", NATSUser("svc"), NATSPass("p&ss w")),
			want: "nats://10.0.0.1:4222/fleet?pass=p%26ss+w&user=svc",
		},
		"eventhub is four ordered parts, not a url": {
			got:  EventHub("sb://acme.servicebus.windows.net/", "writer", "k+ey=", "fleet"),
			want: "Endpoint=sb://acme.servicebus.windows.net/;SharedAccessKeyName=writer;SharedAccessKey=k+ey=;EntityPath=fleet",
		},
		// The API token is the one query parameter Tile38 requires, so it is
		// positional rather than an option.
		"cf-queue carries its token": {
			got:  CFQueue("acct1", "queue1", "tok"),
			want: "cf-queue://acct1/queue1?token=tok",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("url = %q,\n   want %q", tc.got, tc.want)
			}
		})
	}
}
