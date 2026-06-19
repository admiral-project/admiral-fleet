module github.com/admiral-project/admiral/admiral-fleet

go 1.23

replace github.com/admiral-project/admiral/admirald => ../admirald

require (
	github.com/admiral-project/admiral/admirald v0.0.0-00010101000000-000000000000
	github.com/lib/pq v1.10.9
)
