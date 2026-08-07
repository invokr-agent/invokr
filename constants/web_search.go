package constants

import "maps"

const (
	WebSearchToolName   = "web_search"
	WebSearchPolicyTier = "web_network"

	WebSearchDefaultMaxResults = 5
	WebSearchMaxResultsCap     = 20
	WebSearchRankingVersion    = "v1"

	WebSearchProviderBrave      = "brave"
	WebSearchProviderTavily     = "tavily"
	WebSearchProviderCustomHTTP = "custom_http"
	WebSearchProviderSearXNG    = "searxng"

	// SearXNG runs as a local container invokr starts on first use; the host
	// port is bound to loopback only.
	SearXNGDefaultImage  = "searxng/searxng:latest"
	SearXNGDefaultPort   = 8888
	SearXNGDefaultEngine = "docker"
	SearXNGContainerName = "invokr-searxng"
	SearXNGContainerPort = 8080

	BraveSearchEndpoint     = "https://api.search.brave.com/res/v1/web/search"
	BraveSearchAPIKeyEnv    = "BRAVE_SEARCH_API_KEY"
	BraveSearchAPIKeyHeader = "X-Subscription-Token"

	TavilySearchEndpoint     = "https://api.tavily.com/search"
	TavilySearchAPIKeyEnv    = "TAVILY_API_KEY"
	TavilySearchAPIKeyHeader = "Authorization"

	CustomHTTPSearchDefaultMethod     = "GET"
	CustomHTTPSearchDefaultQueryParam = "q"

	WebSearchDefaultCacheDir = ".invokr/cache/web_search"

	// web_fetch shares WebSearchPolicyTier and config.WebSearchConfig.
	WebFetchToolName         = "web_fetch"
	WebFetchUserAgent        = "invokr-agent"
	WebFetchDefaultMaxBytes  = 200000
	WebFetchMaxBytesCap      = 2000000
	WebFetchDefaultTimeoutMS = 20000
	WebFetchMaxTimeoutMS     = 120000
	WebFetchMaxRedirects     = 5
)

// searxng is the default because it is the only provider that works out of
// the box: brave and tavily both need a paid API key, so with no key the
// default list produced a tool that could only fail. searxng needs a docker
// daemon instead, and invokr starts and reuses the container itself.
var defaultWebSearchProviders = []string{
	WebSearchProviderSearXNG,
}

func DefaultWebSearchProviders() []string { return append([]string(nil), defaultWebSearchProviders...) }

var defaultCustomHTTPSearchMappings = map[string]string{
	"results": "$.items", "title": "$.title", "url": "$.link", "snippet": "$.snippet", "published_at": "$.date", "score": "$.score",
}

func DefaultCustomHTTPSearchMappings() map[string]string {
	return maps.Clone(defaultCustomHTTPSearchMappings)
}
