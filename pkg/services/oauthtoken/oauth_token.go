package oauthtoken

import (
	"context"
	"errors"
	"strings"

	"github.com/grafana/grafana/pkg/bus"
	"github.com/grafana/grafana/pkg/infra/log"
	"github.com/grafana/grafana/pkg/login/social"
	"github.com/grafana/grafana/pkg/models"
	"golang.org/x/oauth2"
)

var (
	logger = log.New("oauthtoken")
)

type Service struct {
	SocialService social.Service
}

type OAuthTokenService interface {
	GetCurrentOAuthToken(context.Context, *models.SignedInUser) *oauth2.Token
	IsOAuthPassThruEnabled(*models.DataSource) bool
}

func ProvideService(socialService social.Service) *Service {
	return &Service{
		SocialService: socialService,
	}
}

// GetCurrentOAuthToken returns the OAuth token, if any, for the authenticated user. Will try to refresh the token if it has expired.
func (o *Service) GetCurrentOAuthToken(ctx context.Context, user *models.SignedInUser) *oauth2.Token {
	if user == nil {
		// No user, therefore no token
		return nil
	}

	authInfoQuery := &models.GetAuthInfoQuery{UserId: user.UserId}
	if err := bus.Dispatch(authInfoQuery); err != nil {
		if errors.Is(err, models.ErrUserNotFound) {
			// Not necessarily an error.  User may be logged in another way.
			logger.Debug("no OAuth token for user found", "userId", user.UserId, "username", user.Login)
		} else {
			logger.Error("failed to get OAuth token for user", "userId", user.UserId, "username", user.Login, "error", err)
		}
		return nil
	}

	authProvider := authInfoQuery.Result.AuthModule
	// The socialMap keys don't have "oauth_" prefix, but everywhere else in the system does
	provider := strings.TrimPrefix(authProvider, "oauth_")
	connector, ok := social.SocialMap[provider]
	if !ok {
		logger.Error("failed to find OAuth provider for %q", provider)
		return nil
	}

	client, err := social.GetOAuthHttpClient(authProvider)
	if err != nil {
		logger.Error("failed to get OAuth HTTP client", "provider", authProvider, "error", err)
		return nil
	}
	ctx = context.WithValue(ctx, oauth2.HTTPClient, client)

	persistedToken := &oauth2.Token{
		AccessToken:  authInfoQuery.Result.OAuthAccessToken,
		Expiry:       authInfoQuery.Result.OAuthExpiry,
		RefreshToken: authInfoQuery.Result.OAuthRefreshToken,
		TokenType:    authInfoQuery.Result.OAuthTokenType,
	}
	// TokenSource handles refreshing the token if it has expired
	token, err := connector.TokenSource(ctx, persistedToken).Token()
	if err != nil {
		logger.Error("failed to retrieve OAuth access token", "provider", authInfoQuery.Result.AuthModule,
			"userId", user.UserId, "username", user.Login, "error", err)
		return nil
	}

	// If the tokens are not the same, update the entry in the DB
	if !tokensEq(persistedToken, token) {
		updateAuthCommand := &models.UpdateAuthInfoCommand{
			UserId:     authInfoQuery.Result.UserId,
			AuthModule: authInfoQuery.Result.AuthModule,
			AuthId:     authInfoQuery.Result.AuthId,
			OAuthToken: token,
		}
		if err := bus.Dispatch(updateAuthCommand); err != nil {
			logger.Error("failed to update auth info during token refresh", "userId", user.UserId, "username", user.Login, "error", err)
			return nil
		}
		logger.Debug("updated OAuth info for user", "userId", user.UserId, "username", user.Login)
	}
	return token
}

// IsOAuthPassThruEnabled returns true if Forward OAuth Identity (oauthPassThru) is enabled for the provided data source.
func (o *Service) IsOAuthPassThruEnabled(ds *models.DataSource) bool {
	return ds.JsonData != nil && ds.JsonData.Get("oauthPassThru").MustBool()
}

// tokensEq checks for OAuth2 token equivalence given the fields of the struct Grafana is interested in
func tokensEq(t1, t2 *oauth2.Token) bool {
	return t1.AccessToken == t2.AccessToken &&
		t1.RefreshToken == t2.RefreshToken &&
		t1.Expiry.Equal(t2.Expiry) &&
		t1.TokenType == t2.TokenType
}
