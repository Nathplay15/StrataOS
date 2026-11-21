package s3gw

// Bucket versioning and object lock are set through the gateway's own S3
// API with the root credentials (the gateway validates them and keeps them
// in its metadata), with a small AWS Signature V4 client: no SDK for four
// calls.

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Region is the region the gateway is started with.
const Region = "us-east-1"

// VersionsDir holds one link per versioned bucket to the folder keeping its
// older versions, on the bucket's own volume (--versioning-dir).
var VersionsDir = Dir + "/versions"

// VersionsFolder is the name of that folder at the root of a volume.
const VersionsFolder = ".strata-s3-versions"

// Retention modes of object lock.
const (
	Governance = "GOVERNANCE" // users cannot shorten or remove it; the root account can
	Compliance = "COMPLIANCE" // nobody can, until it expires
)

// Lock is the object lock setting of a bucket: DefaultMode and DefaultDays
// give new objects a retention ("" / 0: none, objects are locked only when
// a client asks for it, or by a legal hold).
type Lock struct {
	Enabled     bool
	DefaultMode string
	DefaultDays int
}

// ValidLock checks an object lock setting.
func ValidLock(l Lock) error {
	switch l.DefaultMode {
	case "":
		if l.DefaultDays != 0 {
			return fmt.Errorf("choose the retention mode")
		}
	case Governance, Compliance:
		if l.DefaultDays < 1 || l.DefaultDays > 36500 {
			return fmt.Errorf("the default retention is 1 to 36500 days")
		}
	default:
		return fmt.Errorf("the retention mode is governance or compliance")
	}
	return nil
}

// Client calls the gateway as its root account.
type Client struct {
	Endpoint string // http(s)://127.0.0.1:port
	Access   string
	Secret   string
	HTTP     *http.Client
	now      func() time.Time
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func hmacSHA256(key []byte, s string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(s))
	return m.Sum(nil)
}

// uriEncode encodes like AWS: unreserved characters kept, the rest %XX.
func uriEncode(s string, slash bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		case c == '/' && !slash:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// sign adds the AWS Signature V4 headers to a request whose body is body.
func sign(req *http.Request, access, secret, region string, body []byte, now time.Time) {
	amzDate := now.UTC().Format("20060102T150405Z")
	day := amzDate[:8]
	payload := sha256Hex(body)
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payload)
	host := req.URL.Host

	var keys []string
	q := req.URL.Query()
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var qs []string
	for _, k := range keys {
		vs := q[k]
		sort.Strings(vs)
		for _, v := range vs {
			qs = append(qs, uriEncode(k, true)+"="+uriEncode(v, true))
		}
	}
	headers := map[string]string{"host": host}
	for k, v := range req.Header {
		lk := strings.ToLower(k)
		if lk == "content-type" || lk == "content-md5" || strings.HasPrefix(lk, "x-amz-") {
			headers[lk] = strings.TrimSpace(strings.Join(v, ","))
		}
	}
	var names []string
	for k := range headers {
		names = append(names, k)
	}
	sort.Strings(names)
	var ch strings.Builder
	for _, k := range names {
		ch.WriteString(k + ":" + headers[k] + "\n")
	}
	signed := strings.Join(names, ";")
	path := req.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	canonical := strings.Join([]string{req.Method, uriEncode(mustUnescape(path), false), strings.Join(qs, "&"), ch.String(), signed, payload}, "\n")
	scope := day + "/" + region + "/s3/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + sha256Hex([]byte(canonical))
	k := hmacSHA256([]byte("AWS4"+secret), day)
	k = hmacSHA256(k, region)
	k = hmacSHA256(k, "s3")
	k = hmacSHA256(k, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(k, toSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+access+"/"+scope+", SignedHeaders="+signed+", Signature="+sig)
}

func mustUnescape(p string) string {
	if u, err := url.PathUnescape(p); err == nil {
		return u
	}
	return p
}

// S3Error is an error answer of the gateway.
type S3Error struct {
	Status  int
	Code    string `xml:"Code"`
	Message string `xml:"Message"`
}

func (e *S3Error) Error() string {
	return fmt.Sprintf("S3 %d %s: %s", e.Status, e.Code, e.Message)
}

func (c *Client) do(ctx context.Context, method, bucket, sub string, body []byte, out any) error {
	if err := ValidBucketName(bucket); err != nil {
		return err
	}
	u := strings.TrimRight(c.Endpoint, "/") + "/" + bucket + "?" + sub
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if len(body) > 0 {
		sum := md5.Sum(body)
		req.Header.Set("Content-MD5", base64.StdEncoding.EncodeToString(sum[:]))
		req.Header.Set("Content-Type", "application/xml")
	}
	now := time.Now
	if c.now != nil {
		now = c.now
	}
	sign(req, c.Access, c.Secret, Region, body, now())
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		e := &S3Error{Status: resp.StatusCode}
		_ = xml.Unmarshal(data, e)
		return e
	}
	if out != nil && len(data) > 0 {
		return xml.Unmarshal(data, out)
	}
	return nil
}

const s3NS = `xmlns="http://s3.amazonaws.com/doc/2006-03-01/"`

// Versioning returns the versioning status of a bucket: "" (never turned
// on), Enabled or Suspended.
func (c *Client) Versioning(ctx context.Context, bucket string) (string, error) {
	var v struct {
		Status string `xml:"Status"`
	}
	err := c.do(ctx, http.MethodGet, bucket, "versioning=", nil, &v)
	return v.Status, err
}

// SetVersioning sets it (Enabled or Suspended).
func (c *Client) SetVersioning(ctx context.Context, bucket, status string) error {
	if status != "Enabled" && status != "Suspended" {
		return fmt.Errorf("invalid versioning status %q", status)
	}
	body := `<VersioningConfiguration ` + s3NS + `><Status>` + status + `</Status></VersioningConfiguration>`
	return c.do(ctx, http.MethodPut, bucket, "versioning=", []byte(body), nil)
}

type lockXML struct {
	Enabled string `xml:"ObjectLockEnabled"`
	Rule    *struct {
		Default struct {
			Mode  string `xml:"Mode"`
			Days  int    `xml:"Days"`
			Years int    `xml:"Years"`
		} `xml:"DefaultRetention"`
	} `xml:"Rule"`
}

// ObjectLock returns the object lock configuration of a bucket (Enabled
// false when it has none).
func (c *Client) ObjectLock(ctx context.Context, bucket string) (Lock, error) {
	var x lockXML
	err := c.do(ctx, http.MethodGet, bucket, "object-lock=", nil, &x)
	var se *S3Error
	if err != nil && asS3Error(err, &se) && (se.Code == "ObjectLockConfigurationNotFoundError" || se.Status == http.StatusNotFound) {
		return Lock{}, nil
	}
	if err != nil {
		return Lock{}, err
	}
	l := Lock{Enabled: x.Enabled == "Enabled"}
	if x.Rule != nil {
		l.DefaultMode, l.DefaultDays = x.Rule.Default.Mode, x.Rule.Default.Days+365*x.Rule.Default.Years
	}
	return l, nil
}

func asS3Error(err error, se **S3Error) bool {
	e, ok := err.(*S3Error)
	if ok {
		*se = e
	}
	return ok
}

// SetObjectLock turns object lock on for a bucket with its default retention.
func (c *Client) SetObjectLock(ctx context.Context, bucket string, l Lock) error {
	if err := ValidLock(l); err != nil {
		return err
	}
	body := `<ObjectLockConfiguration ` + s3NS + `><ObjectLockEnabled>Enabled</ObjectLockEnabled>`
	if l.DefaultMode != "" {
		body += fmt.Sprintf(`<Rule><DefaultRetention><Mode>%s</Mode><Days>%d</Days></DefaultRetention></Rule>`, l.DefaultMode, l.DefaultDays)
	}
	body += `</ObjectLockConfiguration>`
	return c.do(ctx, http.MethodPut, bucket, "object-lock=", []byte(body), nil)
}
