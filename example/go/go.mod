module github.com/jpillora/velox/example/go

go 1.26.8

replace github.com/jpillora/velox => ../..

require (
	github.com/jpillora/sizestr v1.0.0
	github.com/jpillora/velox v0.11.1
	google.golang.org/grpc v1.83.2
)

require (
	github.com/gorilla/websocket v1.5.3 // indirect
	github.com/jpillora/eventsource v1.2.0 // indirect
)
