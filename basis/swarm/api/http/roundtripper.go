//
// (at your option) any later version.
//
//

package http

import (
	"fmt"
	"net/http"

	"github.com/5uwifi/canchain/basis/swarm/log"
)

type RoundTripper struct {
	Host string
	Port string
}

func (self *RoundTripper) RoundTrip(req *http.Request) (resp *http.Response, err error) {
	host := self.Host
	if len(host) == 0 {
		host = "localhost"
	}
	url := fmt.Sprintf("http://%s:%s/%s:/%s/%s", host, self.Port, req.Proto, req.URL.Host, req.URL.Path)
	log.Info(fmt.Sprintf("roundtripper: proxying request '%s' to '%s'", req.RequestURI, url))
	reqProxy, err := http.NewRequest(req.Method, url, req.Body)
	if err != nil {
		return nil, err
	}
	return http.DefaultClient.Do(reqProxy)
}
