package database

import (
	"time"

	"github.com/rs/zerolog/log"
	"github.com/spf13/viper"
	"github.com/streadway/amqp"
)

type Connection struct {
	host       string
	errors     chan *amqp.Error
	connection *amqp.Connection
	channels   []*amqp.Channel
}

type RabbitMQ struct {
	connections []*Connection
}

func NewRabbitMQ() (*RabbitMQ, error) {
	hosts := viper.GetStringSlice("rabbitmq.hosts")
	rmq := &RabbitMQ{}
	for _, host := range hosts {
		rmq.connections = append(rmq.connections, newConnection(host))
	}
	return rmq, nil
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
	c.connection = connection
	c.connection.NotifyClose(c.errors)
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
	if c.connection != nil {
		c.connection.Close()
		c.connection = nil
	}
	c.channels = nil
	close(c.errors)
	c.errors = make(chan *amqp.Error)
}
