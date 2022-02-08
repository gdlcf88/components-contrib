/*
Copyright 2021 The Dapr Authors
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at
    http://www.apache.org/licenses/LICENSE-2.0
Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package rabbitmq

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/streadway/amqp"
	"github.com/stretchr/testify/assert"

	"github.com/dapr/components-contrib/pubsub"
	"github.com/dapr/kit/logger"
)

const (
	testMetadataHostValue       = "anyhost"
	testMetadataConsumerIDValue = "consumer"
)

type FakeAcknowledger struct {
}

func (a FakeAcknowledger) Ack(tag uint64, multiple bool) error {
	return nil
}

func (a FakeAcknowledger) Nack(tag uint64, multiple bool, requeue bool) error {
	return nil
}
func (a FakeAcknowledger) Reject(tag uint64, requeue bool) error {
	return nil
}

func newBroker() *rabbitMQInMemoryBroker {
	return &rabbitMQInMemoryBroker{
		buffer: make(chan amqp.Delivery, 2),
	}
}

func newRabbitMQTest(broker *rabbitMQInMemoryBroker) pubsub.PubSub {
	return &rabbitMQ{
		declaredExchanges: make(map[string]bool),
		stopped:           false,
		logger:            logger.NewLogger("test"),
		connectionDial: func(host string) (rabbitMQConnectionBroker, rabbitMQChannelBroker, error) {
			broker.connectCount++

			return broker, broker, nil
		},
	}
}

func TestNoHost(t *testing.T) {
	broker := newBroker()
	pubsubRabbitMQ := newRabbitMQTest(broker)
	err := pubsubRabbitMQ.Init(pubsub.Metadata{})
	assert.NotNil(t, err)
	assert.Contains(t, err.Error(), "missing RabbitMQ host")
}

func TestNoConsumer(t *testing.T) {
	broker := newBroker()
	pubsubRabbitMQ := newRabbitMQTest(broker)
	metadata := pubsub.Metadata{
		Properties: map[string]string{
			metadataHostKey: testMetadataHostValue,
		},
	}
	err := pubsubRabbitMQ.Init(metadata)
	assert.NoError(t, err)
	err = pubsubRabbitMQ.Subscribe(pubsub.SubscribeRequest{}, nil)
	assert.Contains(t, err.Error(), "consumerID is required for subscriptions")
}

func TestConcurrencyMode(t *testing.T) {
	t.Run("parallel", func(t *testing.T) {
		broker := newBroker()
		pubsubRabbitMQ := newRabbitMQTest(broker)
		metadata := pubsub.Metadata{
			Properties: map[string]string{
				metadataHostKey:       testMetadataHostValue,
				metadataConsumerIDKey: testMetadataConsumerIDValue,
				pubsub.ConcurrencyKey: string(pubsub.Parallel),
			},
		}
		err := pubsubRabbitMQ.Init(metadata)
		assert.Nil(t, err)
		assert.Equal(t, pubsub.Parallel, pubsubRabbitMQ.(*rabbitMQ).metadata.concurrency)
	})

	t.Run("single", func(t *testing.T) {
		broker := newBroker()
		pubsubRabbitMQ := newRabbitMQTest(broker)
		metadata := pubsub.Metadata{
			Properties: map[string]string{
				metadataHostKey:       testMetadataHostValue,
				metadataConsumerIDKey: testMetadataConsumerIDValue,
				pubsub.ConcurrencyKey: string(pubsub.Single),
			},
		}
		err := pubsubRabbitMQ.Init(metadata)
		assert.Nil(t, err)
		assert.Equal(t, pubsub.Single, pubsubRabbitMQ.(*rabbitMQ).metadata.concurrency)
	})

	t.Run("default", func(t *testing.T) {
		broker := newBroker()
		pubsubRabbitMQ := newRabbitMQTest(broker)
		metadata := pubsub.Metadata{
			Properties: map[string]string{
				metadataHostKey:       testMetadataHostValue,
				metadataConsumerIDKey: testMetadataConsumerIDValue,
			},
		}
		err := pubsubRabbitMQ.Init(metadata)
		assert.Nil(t, err)
		assert.Equal(t, pubsub.Parallel, pubsubRabbitMQ.(*rabbitMQ).metadata.concurrency)
	})
}

func TestPublishAndSubscribe(t *testing.T) {
	broker := newBroker()
	pubsubRabbitMQ := newRabbitMQTest(broker)
	metadata := pubsub.Metadata{
		Properties: map[string]string{
			metadataHostKey:       testMetadataHostValue,
			metadataConsumerIDKey: testMetadataConsumerIDValue,
		},
	}
	err := pubsubRabbitMQ.Init(metadata)
	assert.Nil(t, err)
	assert.Equal(t, 1, broker.connectCount)
	assert.Equal(t, 0, broker.closeCount)

	topic := "mytopic"

	messageCount := 0
	lastMessage := ""
	processed := make(chan bool)
	handler := func(ctx context.Context, msg *pubsub.NewMessage) error {
		messageCount++
		lastMessage = string(msg.Data)
		processed <- true

		return nil
	}

	err = pubsubRabbitMQ.Subscribe(pubsub.SubscribeRequest{Topic: topic}, handler)
	assert.Nil(t, err)

	err = pubsubRabbitMQ.Publish(&pubsub.PublishRequest{Topic: topic, Data: []byte("hello world")})
	assert.Nil(t, err)
	<-processed
	assert.Equal(t, 1, messageCount)
	assert.Equal(t, "hello world", lastMessage)

	err = pubsubRabbitMQ.Publish(&pubsub.PublishRequest{Topic: topic, Data: []byte("foo bar")})
	assert.Nil(t, err)
	<-processed
	assert.Equal(t, 2, messageCount)
	assert.Equal(t, "foo bar", lastMessage)
}

func TestPublishReconnect(t *testing.T) {
	broker := newBroker()
	pubsubRabbitMQ := newRabbitMQTest(broker)
	metadata := pubsub.Metadata{
		Properties: map[string]string{
			metadataHostKey:       testMetadataHostValue,
			metadataConsumerIDKey: testMetadataConsumerIDValue,
		},
	}
	err := pubsubRabbitMQ.Init(metadata)
	assert.Nil(t, err)
	assert.Equal(t, 1, broker.connectCount)
	assert.Equal(t, 0, broker.closeCount)

	topic := "othertopic"

	messageCount := 0
	lastMessage := ""
	processed := make(chan bool)
	handler := func(ctx context.Context, msg *pubsub.NewMessage) error {
		messageCount++
		lastMessage = string(msg.Data)
		processed <- true

		return nil
	}

	err = pubsubRabbitMQ.Subscribe(pubsub.SubscribeRequest{Topic: topic}, handler)
	assert.Nil(t, err)

	err = pubsubRabbitMQ.Publish(&pubsub.PublishRequest{Topic: topic, Data: []byte("hello world")})
	assert.Nil(t, err)
	<-processed
	assert.Equal(t, 1, messageCount)
	assert.Equal(t, "hello world", lastMessage)

	err = pubsubRabbitMQ.Publish(&pubsub.PublishRequest{Topic: topic, Data: []byte(errorChannelConnection)})
	assert.NotNil(t, err)
	assert.Equal(t, 1, messageCount)
	assert.Equal(t, "hello world", lastMessage)
	// Check that reconnection happened
	assert.Equal(t, 3, broker.connectCount) // three counts - one initial connection plus 2 reconnect attempts
	assert.Equal(t, 4, broker.closeCount)   // four counts - one for connection, one for channel , times 2 reconnect attempts

	err = pubsubRabbitMQ.Publish(&pubsub.PublishRequest{Topic: topic, Data: []byte("foo bar")})
	assert.Nil(t, err)
	<-processed
	assert.Equal(t, 2, messageCount)
	assert.Equal(t, "foo bar", lastMessage)
}

func TestPublishReconnectAfterClose(t *testing.T) {
	broker := newBroker()
	pubsubRabbitMQ := newRabbitMQTest(broker)
	metadata := pubsub.Metadata{
		Properties: map[string]string{
			metadataHostKey:       testMetadataHostValue,
			metadataConsumerIDKey: testMetadataConsumerIDValue,
		},
	}
	err := pubsubRabbitMQ.Init(metadata)
	assert.Nil(t, err)
	assert.Equal(t, 1, broker.connectCount)
	assert.Equal(t, 0, broker.closeCount)

	topic := "mytopic2"

	messageCount := 0
	lastMessage := ""
	processed := make(chan bool)
	handler := func(ctx context.Context, msg *pubsub.NewMessage) error {
		messageCount++
		lastMessage = string(msg.Data)
		processed <- true

		return nil
	}

	err = pubsubRabbitMQ.Subscribe(pubsub.SubscribeRequest{Topic: topic}, handler)
	assert.Nil(t, err)

	err = pubsubRabbitMQ.Publish(&pubsub.PublishRequest{Topic: topic, Data: []byte("hello world")})
	assert.Nil(t, err)
	<-processed
	assert.Equal(t, 1, messageCount)
	assert.Equal(t, "hello world", lastMessage)

	// Close PubSub
	err = pubsubRabbitMQ.Close()
	assert.Nil(t, err)
	assert.Equal(t, 2, broker.closeCount) // two counts - one for connection, one for channel

	err = pubsubRabbitMQ.Publish(&pubsub.PublishRequest{Topic: topic, Data: []byte(errorChannelConnection)})
	assert.NotNil(t, err)
	assert.Equal(t, 1, messageCount)
	assert.Equal(t, "hello world", lastMessage)
	// Check that reconnection did not happened
	assert.Equal(t, 1, broker.connectCount)
	assert.Equal(t, 2, broker.closeCount) // two counts - one for connection, one for channel
}

func TestSubscribeReconnect(t *testing.T) {
	t.Run("single", testSingleModeSubscribeReconnect)
	t.Run("parallel", testParallelModeSubscribeReconnect)
}

func testSingleModeSubscribeReconnect(t *testing.T) {
	broker := newBroker()
	pubsubRabbitMQ := newRabbitMQTest(broker)
	metadata := pubsub.Metadata{
		Properties: map[string]string{
			metadataHostKey:              testMetadataHostValue,
			metadataConsumerIDKey:        testMetadataConsumerIDValue,
			metadataReconnectWaitSeconds: "0",
			pubsub.ConcurrencyKey:        string(pubsub.Single),
		},
	}
	err := pubsubRabbitMQ.Init(metadata)
	assert.Nil(t, err)
	assert.Equal(t, 1, broker.connectCount)
	assert.Equal(t, 0, broker.closeCount)

	topic := "thetopic"

	messageCount := 0
	lastMessage := ""
	processed := make(chan bool)
	handler := func(ctx context.Context, msg *pubsub.NewMessage) error {
		messageCount++
		lastMessage = string(msg.Data)
		processed <- true

		return errors.New(errorChannelConnection)
	}

	err = pubsubRabbitMQ.Subscribe(pubsub.SubscribeRequest{Topic: topic}, handler)
	assert.Nil(t, err)

	err = pubsubRabbitMQ.Publish(&pubsub.PublishRequest{Topic: topic, Data: []byte("hello world")})
	assert.Nil(t, err)
	<-processed
	assert.Equal(t, 1, messageCount)
	assert.Equal(t, "hello world", lastMessage)

	err = pubsubRabbitMQ.Publish(&pubsub.PublishRequest{Topic: topic, Data: []byte("foo bar")})
	assert.Nil(t, err)
	<-processed
	assert.Equal(t, 2, messageCount)
	assert.Equal(t, "foo bar", lastMessage)

	// allow last reconnect completion
	time.Sleep(time.Second)

	// Check that reconnection happened
	assert.Equal(t, 3, broker.connectCount) // initial connect + 2 reconnects
	assert.Equal(t, 4, broker.closeCount)   // two counts for each connection closure - one for connection, one for channel
}

func testParallelModeSubscribeReconnect(t *testing.T) {
	broker := newBroker()
	pubsubRabbitMQ := newRabbitMQTest(broker)
	metadata := pubsub.Metadata{
		Properties: map[string]string{
			metadataHostKey:              testMetadataHostValue,
			metadataConsumerIDKey:        testMetadataConsumerIDValue,
			metadataReconnectWaitSeconds: "0",
			pubsub.ConcurrencyKey:        string(pubsub.Parallel),
		},
	}
	err := pubsubRabbitMQ.Init(metadata)
	assert.Nil(t, err)
	assert.Equal(t, 1, broker.connectCount)
	assert.Equal(t, 0, broker.closeCount)

	topic := "thetopic"

	messageCount := 0
	lastMessage := ""
	processed := make(chan bool)
	handler := func(ctx context.Context, msg *pubsub.NewMessage) error {
		messageCount++
		lastMessage = string(msg.Data)
		processed <- true

		return errors.New(errorChannelConnection)
	}

	err = pubsubRabbitMQ.Subscribe(pubsub.SubscribeRequest{Topic: topic}, handler)
	assert.Nil(t, err)

	err = pubsubRabbitMQ.Publish(&pubsub.PublishRequest{Topic: topic, Data: []byte("hello world")})
	assert.Nil(t, err)
	err = pubsubRabbitMQ.Publish(&pubsub.PublishRequest{Topic: topic, Data: []byte("hello world")})
	assert.Nil(t, err)
	<-processed
	<-processed
	assert.Equal(t, 2, messageCount)
	assert.Equal(t, "hello world", lastMessage)

	err = pubsubRabbitMQ.Publish(&pubsub.PublishRequest{Topic: topic, Data: []byte("foo bar")})
	assert.Nil(t, err)
	<-processed
	assert.Equal(t, 3, messageCount)
	assert.Equal(t, "foo bar", lastMessage)

	// allow last reconnect completion
	time.Sleep(time.Second)

	// Check that reconnection happened
	assert.Equal(t, 3, broker.connectCount) // initial connect + 2 reconnects
	assert.Equal(t, 4, broker.closeCount)   // two counts for each connection closure - one for connection, one for channel
}

func createAMQPMessage(body []byte) amqp.Delivery {
	return amqp.Delivery{
		Body:         body,
		Acknowledger: FakeAcknowledger{},
	}
}

type rabbitMQInMemoryBroker struct {
	buffer chan amqp.Delivery

	connectCount int
	closeCount   int
}

func (r *rabbitMQInMemoryBroker) Qos(prefetchCount, prefetchSize int, global bool) error {
	return nil
}

func (r *rabbitMQInMemoryBroker) Publish(exchange string, key string, mandatory bool, immediate bool, msg amqp.Publishing) error {
	if string(msg.Body) == errorChannelConnection {
		return errors.New(errorChannelConnection)
	}

	r.buffer <- createAMQPMessage(msg.Body)

	return nil
}

func (r *rabbitMQInMemoryBroker) QueueDeclare(name string, durable bool, autoDelete bool, exclusive bool, noWait bool, args amqp.Table) (amqp.Queue, error) {
	return amqp.Queue{Name: name}, nil
}

func (r *rabbitMQInMemoryBroker) QueueBind(name string, key string, exchange string, noWait bool, args amqp.Table) error {
	return nil
}

func (r *rabbitMQInMemoryBroker) Consume(queue string, consumer string, autoAck bool, exclusive bool, noLocal bool, noWait bool, args amqp.Table) (<-chan amqp.Delivery, error) {
	return r.buffer, nil
}

func (r *rabbitMQInMemoryBroker) Nack(tag uint64, multiple bool, requeue bool) error {
	return nil
}

func (r *rabbitMQInMemoryBroker) Ack(tag uint64, multiple bool) error {
	return nil
}

func (r *rabbitMQInMemoryBroker) ExchangeDeclare(name string, kind string, durable bool, autoDelete bool, internal bool, noWait bool, args amqp.Table) error {
	return nil
}

func (r *rabbitMQInMemoryBroker) Close() error {
	r.closeCount++

	return nil
}
