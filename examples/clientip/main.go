package main

import (
	"fmt"
	"net/http/httptest"

	"github.com/andreunix/devengine/httpx/clientip"
)

func main() {
	resolver, err := clientip.New(
		[]string{"10.20.0.0/16"},
		[]string{
			clientip.HeaderCFConnectingIP,
			clientip.HeaderForwarded,
			clientip.HeaderXForwardedFor,
			clientip.HeaderXRealIP,
		},
	)
	if err != nil {
		panic(err)
	}

	request := httptest.NewRequest("GET", "https://service.example", nil)
	request.RemoteAddr = "10.20.0.4:443"
	request.Header.Set(clientip.HeaderCFConnectingIP, "203.0.113.7")
	fmt.Println(resolver.Resolve(request))
}
