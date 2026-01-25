package velox

import (
	veloxgo "github.com/jpillora/velox/go"
)

type State = veloxgo.State
type Conn = veloxgo.Conn
type Pusher = veloxgo.Pusher
type Client[T any] = veloxgo.Client[T]

var JS = veloxgo.JS
var Sync = veloxgo.Sync
var SyncHandler = veloxgo.SyncHandler
var New = veloxgo.New
var NewAny = veloxgo.NewAny

func NewClient[T any](url string, data *T) (*Client[T], error) {
	return veloxgo.NewClient(url, data)
}
