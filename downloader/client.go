package downloader

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"

	"github.com/PuerkitoBio/goquery"
	"github.com/khorevaa/logos"
)

var releasesURL = "https://releases.1c.ru"
var loginURL = "https://login.1c.ru"

var log = logos.New("github.com/v8platform/oneget/downloader").Sugar()

const (
	projectHrefPrefix    = "/project/"
	tempFileSuffix       = ".d1c"
	casSecurityCheckPath = "/public/security_check"
)

func NewClient(loginUrl string, baseUrl string, login string, password string) (*Client, error) {

	cj, _ := cookiejar.New(nil)

	c := &Client{
		login:    login,
		password: password,
		loginUrl: loginUrl,
		baseUrl:  baseUrl,
		cookie:   cj,
	}

	if err := c.authenticate(); err != nil {
		return nil, err
	}

	return c, nil
}

type Client struct {
	cookie   *cookiejar.Jar
	login    string
	password string
	loginUrl string
	baseUrl  string
}

// authenticate performs the Apereo CAS login flow used by login.1c.ru: fetch the
// login form issued for the releases.1c.ru service, submit credentials together
// with the form's one-time "execution" flow token, and follow the resulting
// service-ticket redirect back into releases.1c.ru. A successful run leaves the
// client's cookie jar holding an authenticated releases.1c.ru session.
func (c *Client) authenticate() error {

	service := c.baseUrl + casSecurityCheckPath
	loginPageURL := c.loginUrl + "/login?" + url.Values{"service": {service}}.Encode()

	req, err := http.NewRequest("GET", loginPageURL, nil)
	if err != nil {
		return err
	}

	resp, err := c.doRequest(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return fmt.Errorf("error parse CAS login page: %s", err.Error())
	}

	execution, ok := doc.Find(`input[name="execution"]`).Attr("value")
	if !ok || execution == "" {
		return fmt.Errorf("error parse CAS login page: <execution> token not found")
	}

	form := url.Values{
		"username":    {c.login},
		"password":    {c.password},
		"execution":   {execution},
		"_eventId":    {"submit"},
		"geolocation": {""},
		"rememberMe":  {"on"},
	}

	authReq, err := http.NewRequest("POST", c.loginUrl+"/login", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	authReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	authResp, err := c.doRequest(authReq)
	if err != nil {
		return err
	}
	defer authResp.Body.Close()

	if c.isLoginPage(authResp) {
		return fmt.Errorf("CAS authentication failed for user <%s>: check the username/password", c.login)
	}

	return nil
}

// isLoginPage reports whether a (possibly redirect-followed) response ended up
// back on login.1c.ru's login form, which is how an expired/missing releases.1c.ru
// session shows up: the site responds with a 302 to CAS and CAS renders the form
// with a fresh 200, rather than an HTTP 401.
func (c *Client) isLoginPage(resp *http.Response) bool {
	return resp.Request != nil &&
		strings.Contains(resp.Request.URL.Host, "login.") &&
		strings.HasPrefix(resp.Request.URL.Path, "/login")
}

func (c *Client) Get(getUrl string) (*http.Response, error) {

	// для URL вида /total/
	if strings.HasPrefix(getUrl, "/") {
		getUrl = c.baseUrl + getUrl
	}

	resp, err := c.getOnce(getUrl)
	if err != nil {
		return nil, err
	}

	if c.isLoginPage(resp) {
		resp.Body.Close()
		log.Debugf("Session expired, re-authenticating for url: %s", getUrl)

		if err := c.authenticate(); err != nil {
			return nil, err
		}

		resp, err = c.getOnce(getUrl)
		if err != nil {
			return nil, err
		}

		if c.isLoginPage(resp) {
			resp.Body.Close()
			return nil, fmt.Errorf("still redirected to CAS login after re-authentication, url: %s", getUrl)
		}
	}

	switch resp.StatusCode {
	case http.StatusBadRequest, http.StatusNotFound:

		return nil, fmt.Errorf("respose CODE:%d  ERR:%s",
			resp.StatusCode, readBodyMustString(resp.Body))
	case http.StatusOK:
		return resp, nil
	default:
		return resp, fmt.Errorf("unknown respose CODE: <%d>", resp.StatusCode)
	}

}

func (c *Client) getOnce(getUrl string) (*http.Response, error) {

	req, err := http.NewRequest("GET", getUrl, nil)
	if err != nil {
		return nil, err
	}

	return c.doRequest(req)
}

func (c *Client) client() *http.Client {

	return &http.Client{
		Jar: c.cookie,
	}
}

func (c *Client) doRequest(req *http.Request) (*http.Response, error) {

	return c.client().Do(req)

}

func readBody(body io.ReadCloser) ([]byte, error) {
	buf := get()
	defer put(buf)
	_, err := io.Copy(buf, body)
	if err != nil {
		return nil, err
	}

	return buf.Bytes(), err
}

func readBodyMustString(body io.ReadCloser) string {
	buf, err := readBody(body)
	if err != nil {
		log.Errorf("must read body err: %s", err.Error())
		return ""
	}

	return string(buf)
}

var pool = sync.Pool{
	New: func() interface{} {
		return &bytes.Buffer{}
	},
}

func get() *bytes.Buffer {
	return pool.Get().(*bytes.Buffer)
}

func put(buf *bytes.Buffer) {
	buf.Reset()
	pool.Put(buf)
}
