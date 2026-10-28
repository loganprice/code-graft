package service

import (
	"context"
	acmesdk "github.com/acme/sdk-go"
	"github.com/other/usersvc"
)

// FetchProfile retrieves the user profile.
func FetchProfile(ctx context.Context, id string) {
	acmeClient := acmesdk.NewClient("secret-token")
	otherClient := usersvc.New()

	// Rewritten because acmeClient comes from github.com/acme/sdk-go
	acmeClient.GetUser(ctx, id)

	// Ignored because otherClient comes from github.com/other/usersvc
	otherClient.GetUser(ctx, id)
}
