//go:generate go-bindata -pkg assets -o assets.go velox.js json-patch.js event-source.js

package assets

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"strconv"
	"strings"
)

//embedded JS file
var veloxJSBytes = append(
	MustAsset("velox.js"),
	append(
		MustAsset("json-patch.js"),
		MustAsset("event-source.js")...,
	)...,
)
var veloxJSBytesCompressed []byte

var VeloxJS = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
	b := veloxJSBytes
	//lazy decompression
	if strings.Contains(req.Header.Get("Accept-Encoding"), "gzip") {
		if veloxJSBytesCompressed == nil {
			buff := bytes.Buffer{}
			g := gzip.NewWriter(&buff)
			g.Write(veloxJSBytes)
			g.Close()
			veloxJSBytesCompressed = buff.Bytes()
		}
		b = veloxJSBytesCompressed
		w.Header().Set("Content-Encoding", "gzip")
	}
	w.Header().Set("Content-Type", "text/javascript")
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.Write(b)
})
