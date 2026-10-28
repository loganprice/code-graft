package rewrite_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/loganprice/code-graft/pkg/rewrite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestByExampleRecipeGenerator(t *testing.T) {
	engine := newTestEngine()

	t.Run("learns Go SDK migration recipe with scope/import diffing and deduplication, then applies to unseen file", func(t *testing.T) {
		beforeGo := `package service

import (
	"context"
	acmesdk "github.com/acme/sdk-go"
)

func FetchOrders(ctx context.Context) {
	client := acmesdk.NewClient("tok_secret")
	client.GetUser(ctx, "user_1")
	client.GetUser(ctx, "user_2")
}
`
		afterGo := `package service

import (
	"context"
	acmesdk "github.com/acme/sdk-go/v2"
)

func FetchOrders(ctx context.Context) {
	client := acmesdk.NewV2Client("tok_secret")
	client.FetchUser(ctx, "user_1", nil)
	client.FetchUser(ctx, "user_2", nil)
}
`
		learned, govReport, err := engine.LearnFromExamples("learned-acme-go-v2", []rewrite.LearnExample{
			{
				Path:   "orders.go",
				Before: []byte(beforeGo),
				After:  []byte(afterGo),
			},
		})
		require.NoError(t, err)
		assert.True(t, govReport.Valid)
		assert.Equal(t, 1, govReport.TestsPassed)

		// The two GetUser call sites should be deduplicated into a single rule, plus the NewClient constructor rule = 2 rules total!
		require.Len(t, learned.Rules, 2)
		assert.Equal(t, "acmesdk.NewClient($ARG1)", learned.Rules[0].Match)
		assert.Equal(t, "acmesdk.NewV2Client($ARG1)", learned.Rules[0].Replace)
		assert.Equal(t, "github.com/acme/sdk-go", learned.Rules[0].FromPackage)

		assert.Equal(t, "$CLIENT.GetUser($ARG1, $ARG2)", learned.Rules[1].Match)
		assert.Equal(t, "$CLIENT.FetchUser($ARG1, $ARG2, nil)", learned.Rules[1].Replace)
		assert.Equal(t, "CLIENT", learned.Rules[1].TargetVar)
		assert.Equal(t, "github.com/acme/sdk-go", learned.Rules[1].FromPackage)

		// Now apply the learned recipe to a brand-new unseen Go file (with different variable names and an unrelated client!)
		unseenGo := `package billing

import (
	"context"
	acmesdk "github.com/acme/sdk-go"
	"github.com/other/pkg"
)

func Charge(reqCtx context.Context, customerID string) {
	myApi := acmesdk.NewClient(getApiKey())
	other := pkg.New()

	myApi.GetUser(reqCtx, customerID)
	other.GetUser(reqCtx, customerID)
}
`
		expectedUnseen := `package billing

import (
	"context"
	"github.com/other/pkg"
	acmesdk "github.com/acme/sdk-go/v2"
)

func Charge(reqCtx context.Context, customerID string) {
	myApi := acmesdk.NewV2Client(getApiKey())
	other := pkg.New()

	myApi.FetchUser(reqCtx, customerID, nil)
	other.GetUser(reqCtx, customerID)
}
`
		res, err := engine.RunOnBytes("billing.go", []byte(unseenGo), learned)
		require.NoError(t, err)
		assert.True(t, res.Changed)
		assert.Equal(t, expectedUnseen, string(res.NewSource))
	})

	t.Run("learns TypeScript variadic $...ARGS promotion across multiple call sites", func(t *testing.T) {
		beforeTS := `import { AcmeClient } from '@acme/sdk-v1';

const api = new AcmeClient("secret");
api.request('/v1/users', { timeout: 500 });
api.request('/v1/health');
`
		afterTS := `import { AcmeV2Client } from '@acme/sdk-v2';

const api = new AcmeV2Client({ token: "secret" });
api.v2Request({ path: '/v1/users' }, { timeout: 500 });
api.v2Request({ path: '/v1/health' });
`
		learned, govReport, err := engine.LearnFromExamples("learned-acme-ts-v2", []rewrite.LearnExample{
			{
				Path:   "client.ts",
				Before: []byte(beforeTS),
				After:  []byte(afterTS),
			},
		})
		require.NoError(t, err)
		assert.True(t, govReport.Valid)
		// Constructor rule + promoted variadic request($ARG1, $...ARGS) rule = 2 rules
		require.Len(t, learned.Rules, 2)
		assert.Equal(t, "$CLIENT.request($ARG1, $...ARGS)", learned.Rules[1].Match)
		assert.Equal(t, "$CLIENT.v2Request({ path: $ARG1 }, $...ARGS)", learned.Rules[1].Replace)
	})

	t.Run("learns recipe directly from a Git commit (LearnFromGitCommit)", func(t *testing.T) {
		repoDir := t.TempDir()
		runGit := func(args ...string) {
			cmd := exec.Command("git", args...)
			cmd.Dir = repoDir
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "git %v failed: %s", args, string(out))
		}

		runGit("init")
		runGit("config", "user.email", "dev@example.com")
		runGit("config", "user.name", "Developer")

		pyPath := filepath.Join(repoDir, "worker.py")
		beforePy := `from acme_sdk import AcmeClient

client = AcmeClient("token_123")
item = client.get_item("item_99")
`
		afterPy := `from acme_sdk_v2 import AcmeV2Client

client = AcmeV2Client(api_token="token_123")
item = client.fetch_item_v2(item_id="item_99")
`
		require.NoError(t, os.WriteFile(pyPath, []byte(beforePy), 0o644))
		runGit("add", "worker.py")
		runGit("commit", "-m", "Initial v1 implementation")

		require.NoError(t, os.WriteFile(pyPath, []byte(afterPy), 0o644))
		runGit("add", "worker.py")
		runGit("commit", "-m", "Migrate worker.py to acme_sdk_v2")

		learned, govReport, err := engine.LearnFromGitCommit(repoDir, "HEAD", "learned-from-git-commit")
		require.NoError(t, err)
		assert.True(t, govReport.Valid)
		assert.Equal(t, 1, govReport.TestsPassed)
		assert.Len(t, learned.Rules, 2)
	})
}
