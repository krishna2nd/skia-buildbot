package auth

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"

	"github.com/skia-dev/glog"
	"go.skia.org/infra/go/httputils"
	"go.skia.org/infra/go/metadata"
	"go.skia.org/infra/go/util"
	"golang.org/x/net/context"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const (
	DEFAULT_JWT_FILENAME           = "service-account.json"
	DEFAULT_CLIENT_SECRET_FILENAME = "client_secret.json"
)

// NewDefaultClient creates a new OAuth 2.0 authorized client with all the
// defaults for the given scopes. If local is true then a 3-legged flow is
// initiated, otherwise the GCE Service Account is used.
//
// The default OAuth config filename is "client_secret.json".
// The default OAuth token store filename is "google_storage_token.data".
func NewDefaultClient(local bool, scopes ...string) (*http.Client, error) {
	return NewClient(local, "", scopes...)
}

// NewClient creates a new OAuth 2.0 authorized client with all the defaults
// for the given scopes, and the given token store filename. If local is true
// then a 3-legged flow is initiated, otherwise the GCE Service Account is
// used.
//
// The default OAuth config filename is "client_secret.json".
func NewClient(local bool, oauthCacheFile string, scopes ...string) (*http.Client, error) {
	return NewClientWithTransport(local, oauthCacheFile, "", nil, scopes...)
}

// NewClientFromIdAndSecret creates a new OAuth 2.0 authorized client with all the defaults
// for the given scopes, and the given token store filename.
func NewClientFromIdAndSecret(clientId, clientSecret, oauthCacheFile string, scopes ...string) (*http.Client, error) {
	config := &oauth2.Config{
		ClientID:     clientId,
		ClientSecret: clientSecret,
		RedirectURL:  "urn:ietf:wg:oauth:2.0:oob",
		Endpoint:     google.Endpoint,
		Scopes:       scopes,
	}
	return NewClientFromConfigAndTransport(true, config, oauthCacheFile, nil)
}

// NewClientWithTransport creates a new OAuth 2.0 authorized client. If local
// is true then a 3-legged flow is initiated, otherwise the GCE Service Account
// is used.
//
// The OAuth tokens will be stored in oauthCacheFile.
// The OAuth config will come from oauthConfigFile.
// The transport will be used. If nil then httputils.NewBackOffTransport() is used.
func NewClientWithTransport(local bool, oauthCacheFile string, oauthConfigFile string, transport http.RoundTripper, scopes ...string) (*http.Client, error) {
	// If this is running locally we need to load the oauth configuration.
	var config *oauth2.Config = nil
	if local {
		if oauthConfigFile == "" {
			oauthConfigFile = DEFAULT_CLIENT_SECRET_FILENAME
		}
		body, err := ioutil.ReadFile(oauthConfigFile)
		if err != nil {
			return nil, err
		}
		config, err = google.ConfigFromJSON(body, scopes...)
		if err != nil {
			return nil, err
		}
	}

	return NewClientFromConfigAndTransport(local, config, oauthCacheFile, transport)
}

// NewClientFromConfigAndTransport creates an new OAuth 2.0 authorized client
// for the given config and transport.
//
// If the transport is nil then httputils.NewBackOffTransport() is used.
// If local is true then a 3-legged flow is initiated, otherwise the GCE
// Service Account is used.
func NewClientFromConfigAndTransport(local bool, config *oauth2.Config, oauthCacheFile string, transport http.RoundTripper) (*http.Client, error) {
	if oauthCacheFile == "" {
		oauthCacheFile = "google_storage_token.data"
	}
	if transport == nil {
		transport = httputils.NewBackOffTransport()
	}

	var client *http.Client
	if local {
		tokenClient := &http.Client{
			Transport: transport,
			Timeout:   httputils.REQUEST_TIMEOUT,
		}
		ctx := context.WithValue(context.Background(), oauth2.HTTPClient, tokenClient)
		tokenSource, err := newCachingTokenSource(oauthCacheFile, ctx, config)
		if err != nil {
			return nil, fmt.Errorf("NewClientFromConfigAndTransport: Unable to create token source: %s", err)
		}
		client = &http.Client{
			Transport: &oauth2.Transport{
				Source: tokenSource,
				Base:   transport,
			},
			Timeout: httputils.REQUEST_TIMEOUT,
		}
	} else {
		// Use compute engine service account.
		client = GCEServiceAccountClient(transport)
	}

	return client, nil
}

const (
	// Supported Cloud storage API OAuth scopes.
	SCOPE_READ_ONLY    = "https://www.googleapis.com/auth/devstorage.read_only"
	SCOPE_READ_WRITE   = "https://www.googleapis.com/auth/devstorage.read_write"
	SCOPE_FULL_CONTROL = "https://www.googleapis.com/auth/devstorage.full_control"
	SCOPE_GCE          = "https://www.googleapis.com/auth/compute"
)

// GCEServiceAccountClient creates an oauth client that is uses the auth token
// attached to an instance in GCE. This requires that the necessary scopes are
// attached to the instance upon creation.  See details here:
// https://cloud.google.com/compute/docs/authentication If transport is nil,
// the default transport will be used.
func GCEServiceAccountClient(transport http.RoundTripper) *http.Client {
	return &http.Client{
		Transport: &oauth2.Transport{
			Source: google.ComputeTokenSource(""),
			Base:   transport,
		},
		Timeout: httputils.REQUEST_TIMEOUT,
	}
}

// cachingTokenSource implments the oauth2.TokenSource interface and
// caches the oauth token in a file.
type cachingTokenSource struct {
	cacheFilePath string
	tokenSource   oauth2.TokenSource
	lastToken     *oauth2.Token
}

// newCachingTokenSource creates a new instance of CachingTokenSource that
// caches the token in cacheFilePath. ctx and config are used to create and
// retrieve the token in the first place.  If no token is available it will run
// though the oauth flow for an installed app.
func newCachingTokenSource(cacheFilePath string, ctx context.Context, config *oauth2.Config) (oauth2.TokenSource, error) {
	var tok *oauth2.Token = nil
	var err error

	if cacheFilePath == "" {
		glog.Warningf("cacheFilePath is empty. Not caching auth token.")
	} else if _, err = os.Stat(cacheFilePath); err == nil {
		// If the file exists. Load from disk.
		f, err := os.Open(cacheFilePath)
		if err != nil {
			return nil, err
		}
		tok = &oauth2.Token{}
		if err = json.NewDecoder(f).Decode(tok); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	// If there was no token, we run through the flow.
	if tok == nil {
		// Run through the flow.
		url := config.AuthCodeURL("state", oauth2.AccessTypeOffline)
		fmt.Printf("Your browser has been opened to visit:\n\n%s\n\nEnter the verification code:", url)

		var code string
		if _, err = fmt.Scan(&code); err != nil {
			return nil, err
		}
		tok, err = config.Exchange(ctx, code)
		if err != nil {
			return nil, err
		}

		if err = saveToken(cacheFilePath, tok); err != nil {
			return nil, err
		}
		glog.Infof("Token saved to %s", cacheFilePath)
	}

	// We have a token at this point.
	tokenSource := config.TokenSource(ctx, tok)
	return &cachingTokenSource{
		cacheFilePath: cacheFilePath,
		tokenSource:   tokenSource,
		lastToken:     tok,
	}, nil
}

// Token is part of implementing the oauth2.TokenSource interface.
func (c *cachingTokenSource) Token() (*oauth2.Token, error) {
	newToken, err := c.tokenSource.Token()
	if err != nil {
		return nil, err
	}

	if newToken.AccessToken != c.lastToken.AccessToken {
		// Write the token to file.
		if err := saveToken(c.cacheFilePath, newToken); err != nil {
			return nil, err
		}
	}

	c.lastToken = newToken
	return newToken, nil
}

func saveToken(cacheFilePath string, tok *oauth2.Token) error {
	if cacheFilePath == "" {
		return nil
	}

	if tok != nil {
		f, err := os.Create(cacheFilePath)
		if err != nil {
			return err
		}
		defer util.Close(f)

		if err := json.NewEncoder(f).Encode(tok); err != nil {
			return err
		}
	}
	return nil
}

// NewDefaultServiceAccountClient Looks for the JWT JSON in metadata, falls
// back to a local file names "service-account.json" if metadata isn't
// available.
func NewDefaultJWTServiceAccountClient(scopes ...string) (*http.Client, error) {
	return NewJWTServiceAccountClient("", "", nil, scopes...)
}

// NewJWTServiceAccountClient creates a new http.Client that is loaded by first
// attempting to load the JWT JSON Service Account data from GCE Project Level
// metadata, and if that fails falls back to loading the data from a local
// file.
//
//   metadataname - The name of the GCE project level metadata key that holds the JWT JSON. If empty a default is used.
//   filename - The name of the local file that holds the JWT JSON. If empty a default is used.
//   transport - A transport. If nil then a default is used.
func NewJWTServiceAccountClient(metadataname, filename string, transport http.RoundTripper, scopes ...string) (*http.Client, error) {
	if metadataname == "" {
		metadataname = metadata.JWT_SERVICE_ACCOUNT
	}
	if filename == "" {
		filename = DEFAULT_JWT_FILENAME
	}
	var body []byte
	jwt, err := metadata.ProjectGet(metadataname)
	if err != nil {
		body, err = ioutil.ReadFile(filename)
		if err != nil {
			return nil, fmt.Errorf("Couldn't find JWT via metadata or in a local file.")
		}
	} else {
		body = []byte(jwt)
	}
	if transport == nil {
		transport = httputils.NewBackOffTransport()
	}
	jwtConfig, err := google.JWTConfigFromJSON(body, scopes...)
	if err != nil {
		return nil, err
	}
	tokenClient := &http.Client{
		Transport: transport,
		Timeout:   httputils.REQUEST_TIMEOUT,
	}
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, tokenClient)
	return &http.Client{
		Transport: &oauth2.Transport{
			Source: jwtConfig.TokenSource(ctx),
			Base:   transport,
		},
		Timeout: httputils.REQUEST_TIMEOUT,
	}, nil
}
