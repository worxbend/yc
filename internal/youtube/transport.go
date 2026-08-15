package youtube

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/worxbend/yc/internal/auth"
)

// absoluteURLPattern finds an absolute URL anywhere in free text.
//
// stripRequestURL removes the URL net/http attaches structurally, as a
// *url.Error. It cannot remove one that an error from another package
// interpolated into its own message, and the OAuth token exchange produces
// exactly that. Nothing this package emits may quote a URL at all - yc's API
// key travels in a query string - so anything shaped like one is replaced
// wholesale rather than merely stripped of its userinfo the way a debug log
// would.
var absoluteURLPattern = regexp.MustCompile(`(?i)\bhttps?://[^\s"'<>]+`)

// maxErrorBodyBytes bounds how much of a failed response is read for error
// detail, so a hostile or broken endpoint cannot make yc buffer a large body.
const maxErrorBodyBytes = 8 << 10

// maxResponseBodyBytes bounds how much of a successful response is decoded.
// The largest legitimate answer - a liveChatMessages.list page holding
// maxResults=2000 messages, each capped at 200 characters plus its metadata -
// stays well under 16 MiB, so the limit is generous headroom, not a tuning
// knob. What it removes is the one unbounded read in the client: without it, a
// misbehaving proxy or a compromised endpoint answering 200 could stream an
// arbitrarily large body straight into the JSON decoder's memory. The endpoint
// is operator-configurable, so "it is always Google" is not a guarantee the
// transport gets to assume.
const maxResponseBodyBytes = 64 << 20

// maxErrorDetailRunes bounds how much of a decoded error message reaches the
// status line. A Google error message is a sentence; anything longer is a
// hostile endpoint, not a diagnostic.
const maxErrorDetailRunes = 200

// liveChatMessage is one item of a liveChatMessages.list response. Wire structs
// stay unexported: nothing outside this package may depend on the YouTube JSON
// shape, which is the whole reason internal/app is allowed to import this
// package at all.
type liveChatMessage struct {
	ID            string                `json:"id"`
	ETag          string                `json:"etag"`
	Snippet       liveChatSnippet       `json:"snippet"`
	AuthorDetails liveChatAuthorDetails `json:"authorDetails"`
}

// liveChatSnippet carries the discriminated event payload. Every details object
// is a pointer so an absent block is distinguishable from an empty one, and the
// dual-shape gift and sticker fields are both present because the discovery
// document and the HTML reference disagree about them.
type liveChatSnippet struct {
	Type              string `json:"type"`
	LiveChatID        string `json:"liveChatId"`
	AuthorChannelID   string `json:"authorChannelId"`
	PublishedAt       string `json:"publishedAt"`
	HasDisplayContent bool   `json:"hasDisplayContent"`
	DisplayMessage    string `json:"displayMessage"`

	TextMessageDetails *struct {
		MessageText string `json:"messageText"`
	} `json:"textMessageDetails"`

	SuperChatDetails    *wireSuperChatDetails    `json:"superChatDetails"`
	SuperStickerDetails *wireSuperStickerDetails `json:"superStickerDetails"`

	// FanFundingEventDetails is the deprecated pre-Super-Chat tip shape. It
	// is decoded defensively only; YouTube has not delivered one since 2017.
	FanFundingEventDetails *wireAmountDetails `json:"fanFundingEventDetails"`

	NewSponsorDetails *struct {
		MemberLevelName string `json:"memberLevelName"`
		IsUpgrade       bool   `json:"isUpgrade"`
	} `json:"newSponsorDetails"`

	MemberMilestoneChatDetails *struct {
		MemberLevelName string `json:"memberLevelName"`
		MemberMonth     int    `json:"memberMonth"`
		UserComment     string `json:"userComment"`
	} `json:"memberMilestoneChatDetails"`

	MembershipGiftingDetails *struct {
		GiftMembershipsCount     int    `json:"giftMembershipsCount"`
		GiftMembershipsLevelName string `json:"giftMembershipsLevelName"`
	} `json:"membershipGiftingDetails"`

	GiftMembershipReceivedDetails *struct {
		MemberLevelName                      string `json:"memberLevelName"`
		GifterChannelID                      string `json:"gifterChannelId"`
		AssociatedMembershipGiftingMessageID string `json:"associatedMembershipGiftingMessageId"`
	} `json:"giftMembershipReceivedDetails"`

	// GiftDetails is the discovery-document shape: a flat object directly on
	// the snippet. GiftEventDetails is the HTML reference's shape, which
	// nests the same fields under giftMetadata. The two official sources
	// disagree, so both are decoded and whichever arrived is used.
	GiftDetails      *wireGiftDetails `json:"giftDetails"`
	GiftEventDetails *struct {
		GiftMetadata wireGiftDetails `json:"giftMetadata"`
	} `json:"giftEventDetails"`

	PollDetails *struct {
		Status   string `json:"status"`
		Metadata struct {
			QuestionText string `json:"questionText"`
			Options      []struct {
				OptionText string `json:"optionText"`
				Tally      string `json:"tally"`
			} `json:"options"`
		} `json:"metadata"`
	} `json:"pollDetails"`

	// MessageDeletedDetails and MessageRetractedDetails were removed from the
	// reference on 2026-06-23 as "not being returned by the API". The enum
	// members survive in the discovery document, so they are decoded
	// opportunistically - but deletion UX keys off tombstone plus local echo.
	MessageDeletedDetails *struct {
		DeletedMessageID string `json:"deletedMessageId"`
	} `json:"messageDeletedDetails"`
	MessageRetractedDetails *struct {
		RetractedMessageID string `json:"retractedMessageId"`
	} `json:"messageRetractedDetails"`

	UserBannedDetails *struct {
		BannedUserDetails  wireChannelProfile `json:"bannedUserDetails"`
		BanType            string             `json:"banType"`
		BanDurationSeconds string             `json:"banDurationSeconds"`
	} `json:"userBannedDetails"`
}

// wireAmountDetails is the shared money block. amountMicros is a uint64 sent as
// a JSON string, so decoding it into a Go int64 directly fails.
type wireAmountDetails struct {
	AmountMicros        string `json:"amountMicros"`
	Currency            string `json:"currency"`
	AmountDisplayString string `json:"amountDisplayString"`
	UserComment         string `json:"userComment"`
}

// wireSuperChatDetails is superChatDetails.
type wireSuperChatDetails struct {
	wireAmountDetails
	Tier int `json:"tier"`
}

// wireSuperStickerDetails is superStickerDetails. Unlike a Super Chat it
// carries no userComment.
type wireSuperStickerDetails struct {
	SuperStickerMetadata struct {
		StickerID string `json:"stickerId"`
		AltText   string `json:"altText"`
		// The discovery document calls this altTextLanguage and the HTML
		// reference calls it language. Accept either.
		AltTextLanguage string `json:"altTextLanguage"`
		Language        string `json:"language"`
	} `json:"superStickerMetadata"`
	AmountMicros        string `json:"amountMicros"`
	Currency            string `json:"currency"`
	AmountDisplayString string `json:"amountDisplayString"`
	Tier                int    `json:"tier"`
}

// wireGiftDetails is the giftEvent payload in either of its two documented
// shapes.
type wireGiftDetails struct {
	GiftName string `json:"giftName"`
	GiftURL  string `json:"giftUrl"`
	// GiftDuration is a protobuf duration string such as "3.5s".
	GiftDuration    string `json:"giftDuration"`
	AltText         string `json:"altText"`
	Language        string `json:"language"`
	JewelsAmount    int    `json:"jewelsAmount"`
	HasVisualEffect bool   `json:"hasVisualEffect"`
	ComboCount      int    `json:"comboCount"`
}

// wireChannelProfile is the ChannelProfileDetails schema, reused by
// userBannedDetails and by the liveChatBan resource.
type wireChannelProfile struct {
	ChannelID       string `json:"channelId"`
	ChannelURL      string `json:"channelUrl"`
	DisplayName     string `json:"displayName"`
	ProfileImageURL string `json:"profileImageUrl"`
}

// liveChatAuthorDetails is the authorDetails block.
type liveChatAuthorDetails struct {
	ChannelID       string `json:"channelId"`
	ChannelURL      string `json:"channelUrl"`
	DisplayName     string `json:"displayName"`
	ProfileImageURL string `json:"profileImageUrl"`
	IsVerified      bool   `json:"isVerified"`
	IsChatOwner     bool   `json:"isChatOwner"`
	IsChatSponsor   bool   `json:"isChatSponsor"`
	IsChatModerator bool   `json:"isChatModerator"`
}

// liveChatMessageListResponse is the liveChatMessages.list envelope. It carries
// more than the commonly documented fields: activePollItem holds the live poll
// out of band, and offlineAt marks the start of the post-broadcast window.
type liveChatMessageListResponse struct {
	NextPageToken     string            `json:"nextPageToken"`
	PollingIntervalMs int               `json:"pollingIntervalMillis"`
	OfflineAt         string            `json:"offlineAt"`
	Items             []liveChatMessage `json:"items"`
	ActivePollItem    *liveChatMessage  `json:"activePollItem"`
	PageInfo          struct {
		TotalResults   int `json:"totalResults"`
		ResultsPerPage int `json:"resultsPerPage"`
	} `json:"pageInfo"`
}

// googleErrorEnvelope is the Google API error body. All three classification
// channels are decoded because they disagree: the legacy errors[].reason, the
// canonical status, and the modern details[] ErrorInfo whose reason is
// SCREAMING_SNAKE.
type googleErrorEnvelope struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
		Errors  []struct {
			Message string `json:"message"`
			Domain  string `json:"domain"`
			Reason  string `json:"reason"`
		} `json:"errors"`
		Details []struct {
			Type   string `json:"@type"`
			Reason string `json:"reason"`
			Domain string `json:"domain"`
		} `json:"details"`
	} `json:"error"`
}

// classify converts a decoded envelope plus its HTTP status into a redacted,
// classified *APIError.
func (e googleErrorEnvelope) classify(statusCode int, method string, redact func(string) string) *APIError {
	var reason string
	if len(e.Error.Errors) > 0 {
		reason = strings.TrimSpace(e.Error.Errors[0].Reason)
	}
	var errorInfoReason string
	for _, detail := range e.Error.Details {
		if strings.HasSuffix(detail.Type, "google.rpc.ErrorInfo") {
			errorInfoReason = strings.TrimSpace(detail.Reason)
			break
		}
	}
	status := strings.TrimSpace(e.Error.Status)

	message := strings.TrimSpace(e.Error.Message)
	if message == "" && len(e.Error.Errors) > 0 {
		message = strings.TrimSpace(e.Error.Errors[0].Message)
	}
	if redact != nil {
		message = redact(message)
	}

	return &APIError{
		StatusCode:      statusCode,
		Reason:          reason,
		Status:          status,
		ErrorInfoReason: errorInfoReason,
		Method:          method,
		Message:         truncateRunes(message, maxErrorDetailRunes),
		sentinel:        ClassifyAPIError(statusCode, reason, status, errorInfoReason),
	}
}

// doJSON performs one API request, refreshing an expired credential once if
// that is what the failure turns out to be, and decodes the response.
//
// It resolves credentials at call time so a mid-session refresh applies, sends
// the API key or the bearer token as appropriate, charges the ledger whether or
// not the call succeeds, and converts a non-2xx response into a classified,
// redacted *APIError. The request URL and the Authorization header must never
// reach the returned error.
//
// There is deliberately no general retry here. Google charges quota for every
// dispatched request including the failures, so a transport that silently
// retries spends the user's day three times as fast; the backoff ladder for
// rate limits, 5xx, and network failures lives in the poller, which can see the
// quota ledger while it decides.
//
// A 401 on a bearer-authenticated call is the one exception, and it is not
// really a retry: a Google access token lasts about an hour and a stream lasts
// longer, so without this the session dies mid-broadcast over a credential yc
// can renew by itself, which the user experiences as several unrelated features
// breaking at once. The refresh is shared by every request that failed in the
// same instant and the request is re-sent exactly once - the second attempt is
// a separate statement below rather than a loop bound, so "exactly once" is a
// property of the shape of this function and not of a counter.
func (c *Client) doJSON(ctx context.Context, method, endpoint, path string, query map[string]string, body, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	token, key := c.credentials()
	if !token.Present() && !key.Present() {
		return newSafeError(endpoint+": "+ErrNoCredentials.Error(), ErrNoCredentials)
	}

	var payload []byte
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return newSafeError(endpoint+": could not encode request", errors.Join(ErrTransient, c.safeCause(err)))
		}
		payload = encoded
	}

	// Sampled before the request goes out, so a refresh that lands while it
	// is on the wire is distinguishable from one this request has to start.
	epoch := c.authEpoch()

	err := c.attempt(ctx, method, endpoint, path, query, payload, out)
	if err == nil || !c.refreshableAuthFailure(err, token) {
		return err
	}

	c.log(ctx, "credential refresh", slog.String("endpoint", endpoint), slog.String("trigger", "http 401"))
	if refreshErr := c.refreshCredentials(ctx, epoch); refreshErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return c.expiredCredentialError(endpoint, err, refreshErr, token)
	}

	retryErr := c.attempt(ctx, method, endpoint, path, query, payload, out)
	if retryErr == nil {
		return nil
	}
	if errors.Is(retryErr, ErrAuthFailed) {
		// A freshly minted token was rejected too. Another exchange would
		// produce another rejection, so this is where it stops.
		return c.expiredCredentialError(endpoint, retryErr, nil, token)
	}
	return retryErr
}

// refreshableAuthFailure reports whether a failure is one a token refresh could
// plausibly fix.
//
// Only a 401 against a bearer-authenticated call qualifies. A rejected API key
// is not renewable, so retrying a key-only read would spend quota to be told
// the same thing; and a 403 is a scope or a role problem, which a new token
// with the same grant will not solve either.
func (c *Client) refreshableAuthFailure(err error, token auth.Secret) bool {
	if c == nil || c.cfg.OnAuthFailure == nil || !token.Present() {
		return false
	}
	if !errors.Is(err, ErrAuthFailed) {
		return false
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusUnauthorized
	}
	return true
}

// expiredCredentialError reports a credential yc could not renew, in terms of
// what the user has to do about it.
//
// refreshErr's identity is dropped on purpose. It comes from the OAuth token
// exchange, whose failures wrap a *url.Error naming the token endpoint and can
// echo an invalid_client body back verbatim, and safeError keeps its cause
// reachable for errors.Is - so retaining it would put that one Unwrap away from
// an output path. Only scrubbed text survives. The API error does stay in the
// chain: it was built by this package and is already redacted.
func (c *Client) expiredCredentialError(endpoint string, cause, refreshErr error, staleToken auth.Secret) error {
	detail := endpoint + ": the sign-in expired and could not be renewed; run `yc login` to sign in again"
	if refreshErr == nil {
		detail = endpoint + ": the API rejected a freshly renewed sign-in; run `yc login` to sign in again"
	} else if reason := c.safeDetail(refreshErr, staleToken); reason != "" {
		detail += " (" + reason + ")"
	}
	return newSafeError(detail, errors.Join(ErrAuthFailed, cause))
}

// safeDetail reduces an error raised outside this package to a one-line
// fragment safe to show, scrubbed of any request URL and of both the stale and
// the current credentials.
func (c *Client) safeDetail(err error, extra ...auth.Secret) string {
	if err == nil {
		return ""
	}
	token, key := c.credentials()
	detail := collapseWhitespace(stripRequestURL(err))
	detail = absoluteURLPattern.ReplaceAllString(detail, auth.RedactedSecret)
	detail = auth.NewRedactor(append([]auth.Secret{token, key}, extra...)...).Redact(detail)
	return truncateRunes(detail, maxErrorDetailRunes)
}

// attempt dispatches one request and decodes one response. It is separate from
// doJSON so the retry after a refresh re-signs and re-sends from scratch: the
// URL, the Authorization header, and the per-attempt timeout all have to be
// rebuilt against the new token rather than reused from the failed try.
func (c *Client) attempt(ctx context.Context, method, endpoint, path string, query map[string]string, body []byte, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	token, _ := c.credentials()
	requestURL, err := c.requestURL(path, query, token.Present())
	if err != nil {
		// The URL carries the API key, so the parse failure is reported
		// without it.
		return newSafeError(endpoint+": could not build request", errors.Join(ErrTransient, c.safeCause(err)))
	}

	var payload io.Reader
	if body != nil {
		payload = bytes.NewReader(body)
	}

	requestCtx := ctx
	if c.cfg.Timeout > 0 {
		var cancel context.CancelFunc
		requestCtx, cancel = context.WithTimeout(ctx, c.cfg.Timeout)
		defer cancel()
	}

	req, err := c.newRequest(requestCtx, method, requestURL, payload)
	if err != nil {
		return newSafeError(endpoint+": could not build request", errors.Join(ErrTransient, c.safeCause(err)))
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	// The ledger is charged before the response is read: Google bills a
	// dispatched request whatever comes back, including an invalid one.
	c.charge(endpoint)

	resp, err := c.httpClient().Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return newSafeError(endpoint+": request failed", errors.Join(ErrTransient, c.safeCause(err)))
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBodyBytes))
		_ = resp.Body.Close()
	}()

	// A redirect is never a legitimate answer from the JSON API, and the
	// client declines to follow one, so it arrives here as a 3xx. Name it
	// rather than letting it decay into a generic transient error that the
	// ladder would retry forever. The Location header is deliberately not
	// quoted: it is attacker-controlled and this package may not emit URLs.
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return newSafeError(endpoint+": the API answered with an unexpected redirect", ErrNotPermitted)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return c.decodeError(resp, endpoint)
	}
	if out == nil {
		return nil
	}
	// The decoder reads through a limit so a 200 with an endless body cannot
	// buffer without bound; a truncated-at-the-limit body fails the decode
	// and is reported like any other malformed response.
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBodyBytes)).Decode(out); err != nil {
		return newSafeError(endpoint+": could not decode response", errors.Join(ErrTransient, c.safeCause(err)))
	}
	return nil
}

// decodeError turns a non-2xx response into a classified *APIError. A body that
// is not a Google error envelope still yields a classified error from the HTTP
// status alone, because an intercepting proxy is not a reason to lose the
// distinction between "rate limited" and "chat ended".
func (c *Client) decodeError(resp *http.Response, endpoint string) error {
	raw := readSmallBody(resp.Body)
	redact := c.redact

	var envelope googleErrorEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Error.Code == 0 && envelope.Error.Status == "" && len(envelope.Error.Errors) == 0 {
		apiErr := &APIError{
			StatusCode: resp.StatusCode,
			Method:     endpoint,
			sentinel:   ClassifyAPIError(resp.StatusCode, "", "", ""),
		}
		if len(raw) > 0 {
			apiErr.Message = truncateRunes(redact(collapseWhitespace(string(raw))), maxErrorDetailRunes)
		}
		return apiErr
	}
	return envelope.classify(resp.StatusCode, endpoint, redact)
}

// readSmallBody reads at most maxErrorBodyBytes for error detail.
func readSmallBody(r io.Reader) []byte {
	if r == nil {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(r, maxErrorBodyBytes))
	if err != nil {
		return data
	}
	return data
}

// newRequest builds a signed request. It exists separately from doJSON so the
// signing and redaction rules have exactly one home.
//
// Exactly one credential is presented: a bearer token when one exists, the API
// key otherwise. Sending both would put a key on the wire that the call does
// not need, and the key is the credential most likely to end up in a log the
// user pastes into an issue.
func (c *Client) newRequest(ctx context.Context, method, url string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if token, _ := c.credentials(); token.Present() {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token.Reveal()))
	}
	return req, nil
}

// requestURL assembles the endpoint URL. The API key travels as a query
// parameter, which is precisely why no error in this package may ever quote a
// URL.
func (c *Client) requestURL(path string, query map[string]string, haveToken bool) (string, error) {
	base, err := url.Parse(c.endpoint())
	if err != nil {
		return "", err
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/" + strings.TrimLeft(path, "/")

	values := base.Query()
	for name, value := range query {
		if strings.TrimSpace(value) == "" {
			continue
		}
		values.Set(name, value)
	}
	if _, key := c.credentials(); !haveToken && key.Present() {
		values.Set("key", strings.TrimSpace(key.Reveal()))
	}
	base.RawQuery = values.Encode()
	return base.String(), nil
}

// collapseWhitespace flattens a body excerpt onto one line so it cannot break
// the status bar's layout.
func collapseWhitespace(value string) string {
	return strings.Join(strings.Fields(value), " ")
}
