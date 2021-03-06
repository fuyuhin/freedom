package requests

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"encoding/xml"
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/http2"
)

var (
	h2cclient *http.Client
)

func init() {
	NewH2cClient(10 * time.Second)
}

// Newh2cClient .
func NewH2cClient(rwTimeout time.Duration) {
	tran := &http2.Transport{
		AllowHTTP: true,
		DialTLS: func(network, addr string, cfg *tls.Config) (net.Conn, error) {
			fun := timeoutDialer(5 * time.Second)
			return fun(network, addr)
		},
	}

	h2cclient = &http.Client{
		Transport: tran,
		Timeout:   rwTimeout,
	}
}

// timeoutDialer returns functions of connection dialer with timeout settings for http.Transport Dial field.
func timeoutDialer(cTimeout time.Duration) func(net, addr string) (c net.Conn, err error) {
	return func(netw, addr string) (net.Conn, error) {
		conn, err := net.DialTimeout(netw, addr, cTimeout)
		if err != nil {
			return nil, err
		}
		return conn, err
	}
}

func NewH2CRequest(rawurl string) Request {
	result := new(H2CRequest)
	req := &http.Request{
		Header: make(http.Header),
	}
	result.resq = req
	result.params = make(map[string]interface{})
	result.url = rawurl
	return result
}

// H2CRequest .
type H2CRequest struct {
	resq   *http.Request
	resp   *http.Response
	reqe   error
	params map[string]interface{}
	url    string
	ctx    context.Context
}

// Post .
func (hr *H2CRequest) Post() Request {
	hr.resq.Method = "POST"
	return hr
}

// Put .
func (hr *H2CRequest) Put() Request {
	hr.resq.Method = "PUT"
	return hr
}

// Get .
func (hr *H2CRequest) Get() Request {
	hr.resq.Method = "GET"
	return hr
}

// Delete .
func (hr *H2CRequest) Delete() Request {
	hr.resq.Method = "DELETE"
	return hr
}

// Head .
func (hr *H2CRequest) Head() Request {
	hr.resq.Method = "HEAD"
	return hr
}

// SetContext .
func (hr *H2CRequest) SetContext(ctx context.Context) Request {
	hr.ctx = ctx
	return hr
}

// SetJSONBody .
func (hr *H2CRequest) SetJSONBody(obj interface{}) Request {
	byts, e := Marshal(obj)
	if e != nil {
		hr.reqe = e
		return hr
	}

	hr.resq.Body = ioutil.NopCloser(bytes.NewReader(byts))
	hr.resq.ContentLength = int64(len(byts))
	hr.resq.Header.Set("Content-Type", "application/json")
	return hr
}

// SetBody .
func (hr *H2CRequest) SetBody(byts []byte) Request {
	hr.resq.Body = ioutil.NopCloser(bytes.NewReader(byts))
	hr.resq.ContentLength = int64(len(byts))
	hr.resq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return hr
}

// ToJSON .
func (hr *H2CRequest) ToJSON(obj interface{}) (r Response) {
	r.Error = hr.do()
	if r.Error != nil {
		return
	}

	hr.httpRespone(&r)
	body := hr.body()
	r.Error = Unmarshal(body, obj)
	if r.Error != nil {
		r.Error = fmt.Errorf("%s, body:%s", r.Error.Error(), string(body))
	}
	return
}

// ToString .
func (hr *H2CRequest) ToString() (value string, r Response) {
	r.Error = hr.do()
	if r.Error != nil {
		return
	}

	hr.httpRespone(&r)
	value = string(hr.body())
	return
}

// ToBytes .
func (hr *H2CRequest) ToBytes() (value []byte, r Response) {
	r.Error = hr.do()
	if r.Error != nil {
		return
	}

	hr.httpRespone(&r)
	value = hr.body()
	return
}

// ToXML .
func (hr *H2CRequest) ToXML(v interface{}) (r Response) {
	r.Error = hr.do()
	if r.Error != nil {
		return
	}

	hr.httpRespone(&r)
	r.Error = xml.Unmarshal(hr.body(), v)
	return
}

// SetParam .
func (hr *H2CRequest) SetParam(key string, value interface{}) Request {
	hr.params[key] = value
	return hr
}

// SetHeader .
func (hr *H2CRequest) SetHeader(key, value string) Request {
	hr.resq.Header.Add(key, value)
	return hr
}

// URI .
func (hr *H2CRequest) URI() string {
	result := hr.url
	if len(hr.params) > 0 {
		uris := strings.Split(result, "?")
		uri := ""
		if len(uris) > 1 {
			uri = uris[1]
		}
		index := 0
		for key, value := range hr.params {
			if index > 0 {
				uri += "&"
			}
			uri += key + "=" + fmt.Sprint(value)
			index++
		}
		result = uris[0] + "?" + uri
	}
	return result
}

func (hr *H2CRequest) do() (e error) {
	if hr.reqe != nil {
		return hr.reqe
	}

	u, e := url.Parse(hr.URI())
	if e != nil {
		return
	}
	hr.resq.URL = u
	waitChanErr := make(chan error)
	defer close(waitChanErr)

	go func(waitErr chan error) {
		var err error
		now := time.Now()
		defer func() {
			code := ""
			pro := ""
			if hr.resp != nil {
				code = strconv.Itoa(hr.resp.StatusCode)
				pro = hr.resp.Proto
			}
			host := ""
			if u, err := url.Parse(hr.url); err == nil {
				host = u.Host
			}
			if err != nil {
				code = "error"
			}
			PrometheusImpl.HttpClientWithLabelValues(host, code, pro, hr.resq.Method, now)
		}()
		hr.resp, err = h2cclient.Do(hr.resq)
		waitErr <- err
	}(waitChanErr)

	if hr.ctx == nil {
		if err := <-waitChanErr; err != nil {
			e = err
			return
		}

		code := hr.resp.StatusCode
		if code >= 400 && code <= 600 {
			e = fmt.Errorf("The FastRequested URL returned error: %d", code)
			return
		}
		return
	}

	select {
	case <-time.After(h2cclient.Timeout):
		e = errors.New("Timeout")
		return
	case <-hr.ctx.Done():
		e = hr.ctx.Err()
		return
	case err := <-waitChanErr:
		if err != nil {
			e = err
			return
		}
		code := hr.resp.StatusCode
		if code >= 400 && code <= 600 {
			e = fmt.Errorf("The FastRequested URL returned error: %d", code)
			return
		}
		return
	}
}

func (hr *H2CRequest) body() (body []byte) {
	defer hr.resp.Body.Close()
	if hr.resp.Header.Get("Content-Encoding") == "gzip" {
		reader, err := gzip.NewReader(hr.resp.Body)
		if err != nil {
			return
		}
		body, _ = ioutil.ReadAll(reader)
		return body
	}
	body, _ = ioutil.ReadAll(hr.resp.Body)
	return
}

func (hr *H2CRequest) httpRespone(httpRespone *Response) {
	httpRespone.StatusCode = hr.resp.StatusCode
	httpRespone.HTTP11 = false
	if hr.resp.ProtoMajor == 1 && hr.resp.ProtoMinor == 1 {
		httpRespone.HTTP11 = true
	}

	httpRespone.ContentLength = hr.resp.ContentLength
	httpRespone.ContentType = hr.resp.Header.Get("Content-Type")
	if httpRespone.Header == nil {
		httpRespone.Header = make(map[string]string)
	}
	for key, values := range hr.resp.Header {
		if len(values) < 1 {
			continue
		}
		httpRespone.Header[key] = values[0]
	}
}
