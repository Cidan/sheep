package rabbitmq

import (
	"encoding/json"
	"time"

	"github.com/Cidan/sheep/database"
	"github.com/rs/zerolog/log"
	"github.com/streadway/amqp"
)

type Connection struct {
	host       string
	errors     chan *amqp.Error
	Connection *amqp.Connection
	Channels   []*Channel
}

type Channel struct {
	Channel *amqp.Channel
}

type RabbitMQ struct {
	connections []*Connection
}

func NewRabbitMQ(hosts []string) (*RabbitMQ, error) {
	rmq := &RabbitMQ{}
	for _, host := range hosts {
		rmq.connections = append(rmq.connections, newConnection(host))
	}
	return rmq, nil
}

// Create a channel for reading from RabbitMQ
func (r *RabbitMQ) Read() (chan *database.Message, error) {
	return nil, nil
}

func (r *RabbitMQ) Save(message *database.Message) error {
	data, err := json.Marshal(&message)
	if err != nil {
		return err
	}
	// TODO: pick at random, check for no connections
	err = r.connections[0].Channels[0].Channel.Publish("sheep", "message", true, false, amqp.Publishing{
		DeliveryMode: amqp.Persistent,
		Timestamp:    time.Now(),
		Body:         data,
	})
	if err != nil {
		return err
	}
	return r.connections[0].Channels[0].Channel.TxCommit()
}

func (r *RabbitMQ) StartWork(db database.Database) {

}

func (r *RabbitMQ) StopWork() {

}

func newConnection(host string) *Connection {
	c := &Connection{
		host:   host,
		errors: make(chan *amqp.Error),
	}
	go c.watch()
	c.dial()
	return c
}

// dial a connection until we connect, and redial on error (with backoff)
func (c *Connection) dial() {
	connection, err := amqp.Dial(c.host)
	if err != nil {
		c.errors <- &amqp.Error{
			Reason: err.Error(),
		}
		return
	}
	c.Connection = connection
	c.Channels = append(c.Channels, newChannel(c))
	c.Connection.NotifyClose(c.errors)
}

// watch a connection handler
func (c *Connection) watch() {
	err := <-c.errors
	log.Error().
		Err(err).
		Str("host", c.host).
		Msg("error on rabbitmq connection")
	// Everything is invalid, reboot.
	c.reset()
	<-time.After(time.Second * 3)
	c.dial()
	go c.watch()
}

func (c *Connection) reset() {
	if c.Connection != nil {
		c.Connection.Close()
		c.Connection = nil
	}
	c.Channels = nil
	close(c.errors)
	c.errors = make(chan *amqp.Error)
}

func newChannel(c *Connection) *Channel {
	channel, err := c.Connection.Channel()

	if err != nil {
		c.errors <- &amqp.Error{
			Reason: err.Error(),
		}
		return nil
	}
	err = channel.Tx()

	if err != nil {
		c.errors <- &amqp.Error{
			Reason: err.Error(),
		}
		return nil
	}
	ch := &Channel{
		Channel: channel,
	}
	ch.Channel.NotifyClose(c.errors)
	channel.ExchangeDeclare("sheep", "direct", true, true, false, true, nil)
	return ch
}