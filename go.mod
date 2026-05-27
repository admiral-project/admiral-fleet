module github.com/admiral-project/admiral/admiral-fleet

go 1.16

replace github.com/admiral-project/admiral/admirald => ../admirald

require (
	github.com/admiral-project/admiral/admirald v0.0.0-00010101000000-000000000000
	github.com/streadway/amqp v1.1.0
)
