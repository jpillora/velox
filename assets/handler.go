//go:generate ./generate.sh

package assets

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"strconv"
	"strings"
)

//embedded JS file
var veloxJSDevelopBytes = MustAsset("dist/velox.js")
var veloxJSBytes = MustAsset("dist/velox.min.js")
var veloxJSBytesGzipped []byte

var VeloxJS = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
	b := veloxJSBytes
	if _, ok := req.URL.Query()["dev"]; ok {
		b = veloxJSDevelopBytes
	} else if strings.Contains(req.Header.Get("Accept-Encoding"), "gzip") {
		//lazy compression
		if veloxJSBytesGzipped == nil {
			buff := bytes.Buffer{}
			g := gzip.NewWriter(&buff)
			g.Write(veloxJSBytes)
			g.Close()
			veloxJSBytesGzipped = buff.Bytes()
		}
		b = veloxJSBytesGzipped
		w.Header().Set("Content-Encoding", "gzip")
	}
	w.Header().Set("Content-Type", "text/javascript")
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.Write(b)
})
