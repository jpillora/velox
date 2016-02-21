package assets

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"strconv"
	"strings"
)

//embedded JS file
var veloxJSBytes = MustAsset("velox.js")
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
