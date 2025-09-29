package rabbit

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"L3_1/models"
	"github.com/streadway/amqp"
)

type Rabbit struct {
	conn    *amqp.Connection
	channel *amqp.Channel
}

const (
	exchangeName   = "notifications_exchange"
	delayQueueName = "notifications_delay_queue"
	mainQueueName  = "notifications_queue"
	deadQueueName  = "notifications_dead_letter"
	routingKeyMain = "notifications.send"
)

var retryQueues = []struct {
	Name string
	TTL  int // ms
}{
	{"notifications_retry_5s", 5000},
	{"notifications_retry_30s", 30000},
	{"notifications_retry_5m", 300000},
}

func New(url string) (*Rabbit, error) {
	conn, err := amqp.Dial(url)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to rabbitmq: %w", err)
	}
	ch, err := conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("failed to open channel: %w", err)
	}

	r := &Rabbit{conn: conn, channel: ch}
	if err := r.setup(); err != nil {
		return nil, err
	}

	return r, nil
}

func NewRabbit(url string) (*Rabbit, error) {
	return New(url)
}

func (r *Rabbit) setup() error {
	// Основной exchange
	if err := r.channel.ExchangeDeclare(
		exchangeName, "direct", true, false, false, false, nil,
	); err != nil {
		return err
	}

	// Основная очередь
	_, err := r.channel.QueueDeclare(mainQueueName, true, false, false, false, nil)
	if err != nil {
		return err
	}
	if err := r.channel.QueueBind(mainQueueName, routingKeyMain, exchangeName, false, nil); err != nil {
		return err
	}

	// Delay queue (для плановой доставки)
	args := amqp.Table{
		"x-dead-letter-exchange":    exchangeName,
		"x-dead-letter-routing-key": routingKeyMain,
	}
	_, err = r.channel.QueueDeclare(delayQueueName, true, false, false, false, args)
	if err != nil {
		return err
	}

	// Dead letter queue (после всех ретраев)
	_, err = r.channel.QueueDeclare(deadQueueName, true, false, false, false, nil)
	if err != nil {
		return err
	}
	if err := r.channel.QueueBind(deadQueueName, "dead", exchangeName, false, nil); err != nil {
		return err
	}

	// Retry-очереди
	for _, rq := range retryQueues {
		args := amqp.Table{
			"x-dead-letter-exchange":    exchangeName,
			"x-dead-letter-routing-key": routingKeyMain,
			"x-message-ttl":             int32(rq.TTL),
		}
		_, err := r.channel.QueueDeclare(rq.Name, true, false, false, false, args)
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *Rabbit) PublishNotification(ctx context.Context, n models.Notification) error {
	body, err := json.Marshal(n)
	if err != nil {
		return err
	}

	delay := time.Until(n.DueAt)
	if delay <= 0 {
		// отправляем сразу в основную очередь
		return r.channel.Publish(
			exchangeName, routingKeyMain, false, false,
			amqp.Publishing{
				ContentType: "application/json",
				Body:        body,
				Headers:     amqp.Table{"x-retry-count": int32(0)},
			},
		)
	}

	// Отправляем в delay queue с TTL
	return r.channel.Publish(
		"", delayQueueName, false, false,
		amqp.Publishing{
			ContentType: "application/json",
			Body:        body,
			Expiration:  fmt.Sprintf("%d", int(delay.Milliseconds())),
			Headers:     amqp.Table{"x-retry-count": int32(0)},
		},
	)
}

func (r *Rabbit) StartProcessing(handler func(notification models.Notification) error) error {
	msgs, err := r.channel.Consume(mainQueueName, "", false, false, false, false, nil)
	if err != nil {
		return err
	}

	go func() {
		for d := range msgs {
			var n models.Notification
			if err := json.Unmarshal(d.Body, &n); err != nil {
				log.Printf("bad message: %v", err)
				d.Nack(false, false)
				continue
			}

			if err := handler(n); err != nil {
				// обработка ошибки — retry
				retryCount := int32(0)
				if val, ok := d.Headers["x-retry-count"].(int32); ok {
					retryCount = val
				}

				if int(retryCount) < len(retryQueues) {
					nextRetry := retryQueues[retryCount]
					nbody := d.Body
					r.channel.Publish(
						"", nextRetry.Name, false, false,
						amqp.Publishing{
							ContentType: "application/json",
							Body:        nbody,
							Headers:     amqp.Table{"x-retry-count": retryCount + 1},
						},
					)
					log.Printf("retry %d scheduled for %s", retryCount+1, nextRetry.Name)
				} else {
					// отправляем в dead-letter
					r.channel.Publish(
						exchangeName, "dead", false, false,
						amqp.Publishing{
							ContentType: "application/json",
							Body:        d.Body,
						},
					)
					log.Printf("message moved to dead-letter")
				}
				d.Ack(false)
				continue
			}

			// Успешно
			d.Ack(false)
		}
	}()

	return nil
}

func (r *Rabbit) Close() error {
	return r.Close()
}
