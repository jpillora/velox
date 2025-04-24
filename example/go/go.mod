module github.com/jpillora/velox/example/go

go 1.24.2

replace github.com/jpillora/velox => ../..

require (
	github.com/NYTimes/gziphandler v1.1.1
	github.com/jpillora/sizestr v1.0.0
	github.com/jpillora/velox v0.4.1
)

require (
	github.com/evanphx/json-patch/v5 v5.9.11 // indirect
	github.com/gorilla/websocket v1.5.3 // indirect
	github.com/jpillora/eventsource v1.1.0 // indirect
)
